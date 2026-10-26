import assert from "node:assert/strict";
import { test } from "node:test";

import { handleEmail, postIngest, type InboundMail } from "./index.ts";

const env = {
  INGEST_URL: "https://mail.example.com/ingest",
  INGEST_TOKEN: "test-ingest-token",
};

function chunks(): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode("From: a@example.com\r\n\r\n"));
      controller.enqueue(encoder.encode("hello"));
      controller.close();
    },
  });
}

function mail(raw: ReadableStream | null = chunks()): InboundMail & { rejected: string } {
  const message = {
    from: "sender@example.com",
    to: "box@richrich.cc.cd",
    raw,
    rejected: "",
    setReject(reason: string) {
      message.rejected = reason;
    },
  };
  return message;
}

test("buffers raw once and posts it with the bearer token", async () => {
  let calls = 0;
  const message = mail();
  await handleEmail(message, env, async (_url, init) => {
    calls += 1;
    assert.equal(calls, 1);
    const headers = new Headers(init?.headers);
    assert.equal(headers.get("Authorization"), "Bearer test-ingest-token");
    assert.equal(headers.get("Content-Type"), "message/rfc822");
    assert.equal(headers.get("X-Envelope-To"), "box@richrich.cc.cd");
    assert.equal(headers.get("X-Envelope-From"), "sender@example.com");
    assert.equal(init?.method, "POST");
    assert.equal(init?.redirect, "manual");
    const posted = new TextDecoder().decode(init?.body as ArrayBuffer);
    assert.equal(posted, "From: a@example.com\r\n\r\nhello");
    return new Response("ok", { status: 201 });
  });
  assert.equal(calls, 1);
  assert.equal(message.rejected, "");
});

test("setReject on permanent ingest failure", async () => {
  const unknown = mail();
  await handleEmail(unknown, env, async () => new Response("no", { status: 422 }));
  assert.equal(unknown.rejected, "Recipient address rejected");

  const large = mail();
  await handleEmail(large, env, async () => new Response("no", { status: 413 }));
  assert.equal(large.rejected, "Message too large");

  const missing = mail();
  await handleEmail(missing, env, async () => new Response("no", { status: 404 }));
  assert.equal(missing.rejected, "Message rejected");
});

test("throws on temporary failure without setReject", async () => {
  for (const status of [401, 500, 503]) {
    const message = mail();
    await assert.rejects(
      () => handleEmail(message, env, async () => new Response("no", { status })),
      /ingest temporary failure/,
    );
    assert.equal(message.rejected, "");
  }
  const message = mail();
  await assert.rejects(
    () =>
      handleEmail(message, env, async () => {
        throw new Error("connect refused");
      }),
    /ingest temporary failure/,
  );
  assert.equal(message.rejected, "");
});

test("missing secret or url rejects before fetch", async () => {
  let calls = 0;
  const fetchImpl: typeof fetch = async () => {
    calls += 1;
    return new Response("no");
  };
  const noToken = mail();
  await handleEmail(noToken, { INGEST_URL: env.INGEST_URL, INGEST_TOKEN: "" }, fetchImpl);
  const noURL = mail();
  await handleEmail(noURL, { INGEST_URL: "   ", INGEST_TOKEN: env.INGEST_TOKEN }, fetchImpl);
  const badURL = mail();
  await handleEmail(badURL, { INGEST_URL: "ftp://files.example/ingest", INGEST_TOKEN: env.INGEST_TOKEN }, fetchImpl);
  assert.equal(calls, 0);
  assert.equal(noToken.rejected, "mail ingest is not configured");
  assert.equal(noURL.rejected, "mail ingest is not configured");
  assert.equal(badURL.rejected, "mail ingest is not configured");
});

test("redirect is a permanent reject and the token stays on the original request", async () => {
  const result = await postIngest(env, "a@richrich.cc.cd", "b@example.com", new ArrayBuffer(0), async () =>
    new Response(null, { status: 302, headers: { Location: "https://elsewhere.example/ingest" } }),
  );
  assert.deepEqual(result, { action: "reject", reason: "Message rejected" });
});
