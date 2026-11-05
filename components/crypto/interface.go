package crypto

type ICrypto interface {
	Encrypt(textBytes []byte) ([]byte, error)
	Decrypt(cipherText []byte) ([]byte, error)
}
