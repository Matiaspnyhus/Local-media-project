package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "correct horse") {
		t.Fatal("right password rejected")
	}
	if VerifyPassword(h, "wrong horse") || VerifyPassword("garbage", "x") || VerifyPassword("", "") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Fatal("two hashes of one password are identical (no salt?)")
	}
}

func TestTokens(t *testing.T) {
	s, h := NewToken()
	if HashToken(s) != h || s == h || len(s) < 40 {
		t.Fatal("bad token")
	}
}

func TestLinkSignature(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	sig := SignLink(key, "file1", 1000, "sess1")
	if !VerifyLink(key, "file1", 1000, "sess1", sig) {
		t.Fatal("good link rejected")
	}
	for _, bad := range []bool{
		VerifyLink(key, "file2", 1000, "sess1", sig),
		VerifyLink(key, "file1", 1001, "sess1", sig),
		VerifyLink(key, "file1", 1000, "sess2", sig),
		VerifyLink([]byte("another key another key another k"), "file1", 1000, "sess1", sig),
		VerifyLink(key, "file1", 1000, "sess1", sig+"A"),
		VerifyLink(key, "file1", 1000, "sess1", ""),
	} {
		if bad {
			t.Fatal("altered link accepted")
		}
	}
}
