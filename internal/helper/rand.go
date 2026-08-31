package helper

import (
	crand "crypto/rand"
	"math/big"
)

const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randomName 生成 n 位随机文件名（小写字母+数字），使用 crypto/rand
func randomName(n int) string {
	max := big.NewInt(int64(len(alphabet)))
	b := make([]byte, n)
	for i := range n {
		idx, err := crand.Int(crand.Reader, max)
		if err != nil {
			b[i] = alphabet[0]
			continue
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b)
}
