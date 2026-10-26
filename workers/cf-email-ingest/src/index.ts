import type { ExportedHandler } from "@cloudflare/workers-types";

// InboundMail is the slice of ForwardableEmailMessage this Worker uses.
// message.raw is a single-use stream; callers must buffer it once.
export interface InboundMail {
  readonly from: string;
  readonly to: string;
  readonly raw: ReadableStream | null;
  setReject(reason: string): void;
}

export type IngestResult =
  | { action: "accept" }
  | { action: "reject"; reason: string }
  | { action: "retry"; reason: string };

const REJECT_UNKNOWN = "Recipient address rejected";
const REJECT_MESSAGE = "Message rejected";
const REJECT_LARGE = "Message too large";
const REJECT_CONFIG = "mail ingest is not configured";
const REJECT_READ = "unable to read message";

export function ingestConfig(env: Env): { url: string; token: string } | null {
  const url = env.INGEST_URL?.trim() ?? "";
  const token = env.INGEST_TOKEN?.trim() ?? "";
  if (url === "" || token === "") {
    return null;
  }
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return null;
  }
  if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
    return null;
  }
  return { url, token };
}

// postIngest sends one buffered RFC822 body. It does not read message.raw.
export async function postIngest(
  env: Env,
  to: string,
  from: string,
  raw: ArrayBuffer,
  fetchImpl: typeof fetch,
): Promise<IngestResult> {
  const cfg = ingestConfig(env);
  if (!cfg) {
    return { action: "reject", reason: REJECT_CONFIG };
  }
  let response: Response;
  try {
    response = await fetchImpl(cfg.url, {
      method: "POST",
      redirect: "manual",
      headers: {
        Authorization: `Bearer ${cfg.token}`,
        "Content-Type": "message/rfc822",
        "X-Envelope-To": headerValue(to),
        "X-Envelope-From": headerValue(from),
      },
      body: raw,
    });
  } catch (err) {
    return { action: "retry", reason: errorText(err) };
  }
  await discard(response);
  if (response.status >= 200 && response.status < 300) {
    return { action: "accept" };
  }
  if (isTemporary(response.status)) {
    return { action: "retry", reason: `ingest status ${response.status}` };
  }
  return { action: "reject", reason: rejectReason(response.status) };
}

// Permanent 4xx matches SMTP reject. 401 and 5xx are temporary so Email
// Routing can retry instead of bouncing mail during a token or database blip.
function isTemporary(status: number): boolean {
  return status === 401 || status === 408 || status === 425 || status === 429 || status >= 500;
}

function rejectReason(status: number): string {
  if (status === 413) {
    return REJECT_LARGE;
  }
  if (status === 422) {
    return REJECT_UNKNOWN;
  }
  return REJECT_MESSAGE;
}

function headerValue(value: string): string {
  return value.replace(/[\r\n\0]/g, "").slice(0, 320);
}

function errorText(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}

export async function handleEmail(
  message: InboundMail,
  env: Env,
  fetchImpl: typeof fetch = fetch,
): Promise<void> {
  if (!ingestConfig(env)) {
    console.error(JSON.stringify({ message: "ingest not configured", to: message.to }));
    message.setReject(REJECT_CONFIG);
    return;
  }
  let raw: ArrayBuffer;
  try {
    // Buffer once. A second read of message.raw is empty.
    raw = await new Response(message.raw ?? emptyStream()).arrayBuffer();
  } catch (err) {
    console.error(
      JSON.stringify({ message: "read raw failed", to: message.to, error: errorText(err) }),
    );
    message.setReject(REJECT_READ);
    return;
  }
  const result = await postIngest(env, message.to, message.from, raw, fetchImpl);
  if (result.action === "accept") {
    console.log(JSON.stringify({ message: "ingested", to: message.to, bytes: raw.byteLength }));
    return;
  }
  if (result.action === "reject") {
    console.error(
      JSON.stringify({
        message: "ingest rejected",
        to: message.to,
        reason: result.reason,
        bytes: raw.byteLength,
      }),
    );
    message.setReject(result.reason);
    return;
  }
  console.error(
    JSON.stringify({
      message: "ingest temporary failure",
      to: message.to,
      reason: result.reason,
      bytes: raw.byteLength,
    }),
  );
  throw new Error("ingest temporary failure");
}

function emptyStream(): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(controller) {
      controller.close();
    },
  });
}

async function discard(response: Response): Promise<void> {
  try {
    await response.body?.cancel();
  } catch {
    // The status is what decides accept, reject, or retry.
  }
}

export default {
  async email(message, env, _ctx): Promise<void> {
    await handleEmail(message, env, fetch);
  },
} satisfies ExportedHandler<Env>;
