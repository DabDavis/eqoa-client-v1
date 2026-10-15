package network

import (
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// Login performs TCP authentication against the auth server.
// Returns the account ID and 32-byte session token on success.
func Login(host string, port int, username, password string) (uint32, []byte, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return 0, nil, fmt.Errorf("auth dial failed: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	// Step 1: Read Server Hello (40 bytes: length + opcode 0x04 + 32-byte challenge)
	hello := make([]byte, 40)
	if _, err := io.ReadFull(conn, hello); err != nil {
		return 0, nil, fmt.Errorf("auth hello read failed: %w", err)
	}
	helloOpcode := binary.BigEndian.Uint32(hello[4:8])
	if helloOpcode != 0x04 {
		return 0, nil, fmt.Errorf("unexpected hello opcode: 0x%08X", helloOpcode)
	}
	log.Printf("Auth: received server hello (challenge)")

	// Step 2: Build login request packet.
	// Wire format (from eqoa.live pcap):
	//   [0-4]   length: 148 (self-inclusive)
	//   [4-8]   opcode: 0x24 (Login Request)
	//   [8-12]  unknown: 0
	//   [12-16] version: 2
	//   [16-48] username (32 bytes null-padded)
	//   [48-80] password DES-CBC encrypted (32 bytes)
	//   [80-84] unknown: 1
	//   [84-88] game ID: "EQOA"
	//   [88-148] padding (zeros)
	const packetSize = 148
	request := make([]byte, packetSize)

	// Packet length (self-inclusive)
	binary.BigEndian.PutUint32(request[0:4], packetSize)
	// Message type: LOGIN_REQUEST (0x24)
	binary.BigEndian.PutUint32(request[4:8], 0x24)
	// Version at offset 12
	binary.BigEndian.PutUint32(request[12:16], 2)
	// Username at absolute offset 16
	copy(request[16:48], []byte(username))
	// Password DES-CBC encrypted at absolute offset 48
	// PS2 client uses DES-CBC with key derived from "LPSS-V1.0$@Z>.ADJ"
	// via OpenSSL des_string_to_key. Key and IV are both the same 8 bytes.
	desKey := []byte{0xdf, 0xc2, 0x04, 0x6b, 0x49, 0x83, 0xfd, 0x6b}
	passBuf := make([]byte, 32) // zero-filled, password null-terminated
	copy(passBuf, []byte(password))
	block, err := des.NewCipher(desKey)
	if err != nil {
		return 0, nil, fmt.Errorf("DES cipher init failed: %w", err)
	}
	cbc := cipher.NewCBCEncrypter(block, desKey) // IV = key (same as PS2)
	cbc.CryptBlocks(passBuf, passBuf)
	copy(request[48:80], passBuf)
	// Unknown field at offset 80
	binary.BigEndian.PutUint32(request[80:84], 1)
	// Game ID "EQOA" at offset 84
	copy(request[84:88], []byte("EQOA"))

	if _, err := conn.Write(request); err != nil {
		return 0, nil, fmt.Errorf("auth write failed: %w", err)
	}

	// Read 168-byte response
	response := make([]byte, 168)
	if _, err := io.ReadFull(conn, response); err != nil {
		return 0, nil, fmt.Errorf("auth read failed: %w", err)
	}

	// Parse response:
	//   offset 4-7: message type (should be 0x25 LOGIN_RESPONSE)
	//   offset 8-11: error flag (0=success, 1=failed)
	//   offset 80-83: account ID
	//   offset 84-87: status (3=success, 4=bad password/already online)
	msgType := binary.BigEndian.Uint32(response[4:8])
	if msgType != 0x25 {
		return 0, nil, fmt.Errorf("unexpected auth response type: 0x%08X", msgType)
	}

	errorFlag := binary.BigEndian.Uint32(response[8:12])
	accountID := binary.BigEndian.Uint32(response[80:84])
	status := binary.BigEndian.Uint32(response[84:88])
	log.Printf("Auth response detail: errorFlag=%d, accountID=%d, status=%d", errorFlag, accountID, status)
	// [96:112] is the session token, which is not logged: the log goes into
	// every F3 report (cmd/client/report.go).
	log.Printf("Auth response hex [8:12]=%X [80:88]=%X",
		response[8:12], response[80:88])

	result := binary.BigEndian.Uint16(response[84:86])

	// Extract 16-byte ASCII session token from response offset 96-112
	sessionTokenASCII := response[96:112]
	tokenEnd := 16
	for i := 0; i < 16; i++ {
		if sessionTokenASCII[i] == 0 {
			tokenEnd = i
			break
		}
	}

	log.Printf("Auth response: accountID=%d, result=0x%04X, sessionToken=%d bytes",
		accountID, result, tokenEnd)

	// Result codes from server: 0x0000 = success, 0x0003 = success (Sandstorm), 0x0008 = success
	if result != 0x0000 && result != 0x0003 && result != 0x0008 {
		return 0, nil, fmt.Errorf("auth failed: result=0x%04X", result)
	}

	// The PS2 client AES-256-ECB encrypts the password with a static key
	// for the UDP Authenticate/Authenticate2 token.
	// Key is at 0x00353B10 in EE RAM, derived from Rijndael key schedule.
	aesKey := []byte{
		0xec, 0xca, 0xbf, 0x71, 0xf4, 0xbf, 0x32, 0x6b,
		0x57, 0x29, 0x72, 0x4d, 0xc1, 0xd7, 0xaa, 0xac,
		0x53, 0x84, 0x37, 0xda, 0x68, 0xe4, 0x3b, 0xb4,
		0xa5, 0xe4, 0xa4, 0xe9, 0xd4, 0x89, 0x6d, 0x44,
	}
	udpPlain := make([]byte, 32) // zero-filled, password null-terminated
	copy(udpPlain, []byte(password))
	aesBlock, aesErr := aes.NewCipher(aesKey)
	if aesErr != nil {
		return 0, nil, fmt.Errorf("AES cipher init failed: %w", aesErr)
	}
	// AES-256-ECB: encrypt two 16-byte blocks independently
	udpToken := make([]byte, 32)
	aesBlock.Encrypt(udpToken[0:16], udpPlain[0:16])
	aesBlock.Encrypt(udpToken[16:32], udpPlain[16:32])
	// Not the token itself: the key is in this file, so the token IS the
	// password to anybody who reads the log.
	log.Printf("Auth: using AES-256-ECB encrypted password as UDP token (%d bytes)", len(udpToken))

	return accountID, udpToken, nil
}

// verantPatcher is the XML structure from the patch server.
type verantPatcher struct {
	XMLName xml.Name `xml:"VerantPatcher"`
	Product struct {
		Name   string `xml:"Name"`
		Server string `xml:"Server"`
		Port   string `xml:"Port"`
	} `xml:"Product"`
}

// CheckPatch queries the HTTP patch server to get the game server IP.
func CheckPatch(host string, port int) (string, error) {
	url := fmt.Sprintf("http://%s:%d/patch/eqoa/eqoa-frontierslive-update.xml.gz", host, port)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("patch request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("patch server returned status %d", resp.StatusCode)
	}

	// Decompress gzip
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("gzip decompress failed: %w", err)
	}
	defer gz.Close()

	xmlData, err := io.ReadAll(gz)
	if err != nil {
		return "", fmt.Errorf("reading patch xml failed: %w", err)
	}

	var patcher verantPatcher
	if err := xml.Unmarshal(xmlData, &patcher); err != nil {
		return "", fmt.Errorf("parsing patch xml failed: %w", err)
	}

	serverIP := patcher.Product.Server
	if serverIP == "" {
		return "", fmt.Errorf("no server IP in patch response")
	}

	log.Printf("Patch server returned game server IP: %s", serverIP)
	return serverIP, nil
}
