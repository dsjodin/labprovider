package deploy

import (
	"crypto/md5"
	"crypto/rand"
	"fmt"
)

// apr1Crypt implements Apache's APR1-MD5 password scheme ($apr1$), htpasswd's
// default and universally supported by nginx's auth_basic. This replaces the
// apache2-utils host dependency the bash depot module had.
func apr1Crypt(password, salt string) string {
	const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	initial := md5.New()
	initial.Write([]byte(password + "$apr1$" + salt))

	alt := md5.Sum([]byte(password + salt + password))
	for i := len(password); i > 0; i -= 16 {
		if i > 16 {
			initial.Write(alt[:16])
		} else {
			initial.Write(alt[:i])
		}
	}
	for i := len(password); i > 0; i >>= 1 {
		if i&1 == 1 {
			initial.Write([]byte{0})
		} else {
			initial.Write([]byte(password[:1]))
		}
	}
	sum := initial.Sum(nil)

	for i := 0; i < 1000; i++ {
		h := md5.New()
		if i&1 == 1 {
			h.Write([]byte(password))
		} else {
			h.Write(sum)
		}
		if i%3 != 0 {
			h.Write([]byte(salt))
		}
		if i%7 != 0 {
			h.Write([]byte(password))
		}
		if i&1 == 1 {
			h.Write(sum)
		} else {
			h.Write([]byte(password))
		}
		sum = h.Sum(nil)
	}

	// APR1's custom base64 ordering.
	encode := func(a, b, c byte, n int) string {
		v := uint(a)<<16 | uint(b)<<8 | uint(c)
		out := make([]byte, n)
		for i := 0; i < n; i++ {
			out[i] = itoa64[v&0x3f]
			v >>= 6
		}
		return string(out)
	}
	encoded := encode(sum[0], sum[6], sum[12], 4) +
		encode(sum[1], sum[7], sum[13], 4) +
		encode(sum[2], sum[8], sum[14], 4) +
		encode(sum[3], sum[9], sum[15], 4) +
		encode(sum[4], sum[10], sum[5], 4) +
		encode(0, 0, sum[11], 2)

	return "$apr1$" + salt + "$" + encoded
}

// htpasswdLine builds one "user:hash" line with a random 8-char salt.
func htpasswdLine(user, password string) (string, error) {
	const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	salt := make([]byte, 8)
	for i, b := range raw {
		salt[i] = itoa64[int(b)%len(itoa64)]
	}
	return fmt.Sprintf("%s:%s\n", user, apr1Crypt(password, string(salt))), nil
}
