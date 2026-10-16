package patcher

import "strings"

// cstr is a C string as the patcher holds one: nil is NULL, which is not the
// same as "" (an attribute that is present and empty). The difference shows:
// a <File RemotePath=""> builds its URL as "/" + Name, one with no RemotePath
// as Name alone.
type cstr = *string

func cs(s string) cstr { return &s }

func str(s cstr) string {
	if s == nil {
		return ""
	}
	return *s
}

// PullXMLAttribute (0x00589B50) is xmlGetProp.
func PullXMLAttribute(n *xmlNode, name string) cstr {
	return xmlGetProp(n, name)
}

// PullXMLAttributeLong (0x00589B70) is atoi on the attribute, 0 when absent.
func PullXMLAttributeLong(n *xmlNode, name string) int32 {
	s := xmlGetProp(n, name)
	if s == nil {
		return 0
	}
	return atoi(*s)
}

// PullXMLAttributeBool (0x00589C20): "y", "yes", "t", "true" or "1",
// case-blind, is true; anything else, an absent attribute included, false.
func PullXMLAttributeBool(n *xmlNode, name string) bool {
	s := xmlGetProp(n, name)
	if s == nil {
		return false
	}
	switch strings.ToLower(*s) {
	case "y", "yes", "t", "true", "1":
		return true
	}
	return false
}

// PullXMLAttributeBoolDef (0x00589CD0): the yes words (now with "on") are
// true, the no words false, and anything else keeps the default.
func PullXMLAttributeBoolDef(n *xmlNode, name string, def bool) bool {
	switch PullDefinedXMLAttBool(n, name) {
	case 1:
		return true
	case 0:
		return false
	}
	return def
}

// PullDefinedXMLAttBool (0x00589E10): 1 yes, 0 no, 2 absent or neither.
func PullDefinedXMLAttBool(n *xmlNode, name string) int {
	s := xmlGetProp(n, name)
	if s == nil {
		return 2
	}
	switch strings.ToLower(*s) {
	case "y", "yes", "t", "true", "on", "1":
		return 1
	case "n", "no", "f", "false", "off", "0":
		return 0
	}
	return 2
}

// atoi is newlib's, (int)strtol(s, 0, 10), and on the EE a long is 64 bits:
// a CRC above 2^31 written in decimal comes back as its low 32 bits, which is
// what the manifest means by it. strtol saturates at LONG_MAX / LONG_MIN.
func atoi(s string) int32 {
	i := 0
	for i < len(s) && (s[i] == ' ' || (s[i] >= '\t' && s[i] <= '\r')) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	limit := uint64(1<<63 - 1)
	if neg {
		limit = 1 << 63
	}
	var v uint64
	over := false
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		d := uint64(s[i] - '0')
		if over || v > (limit-d)/10 {
			over = true
			continue
		}
		v = v*10 + d
	}
	if over {
		v = limit
	}
	if neg {
		return int32(-int64(v))
	}
	return int32(int64(v))
}
