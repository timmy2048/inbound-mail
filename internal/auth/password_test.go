package auth

import "testing"

func TestCheckPlainAndBcrypt(t *testing.T) {
	if !Check("secret", "secret") {
		t.Fatal("plain match")
	}
	if Check("secret", "other") || Check("", "secret") {
		t.Fatal("plain mismatch should fail")
	}
	hash, err := Hash("secret")
	if err != nil {
		t.Fatal(err)
	}
	if !Check(hash, "secret") {
		t.Fatal("bcrypt match")
	}
	if Check(hash, "other") {
		t.Fatal("bcrypt mismatch")
	}
}
