package patcher

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// The patcher walks libxml2 2.3.6's tree directly (node->children,
// node->next, node->name), so the tree here keeps what that one keeps: every
// text node, whitespace included (xmlKeepBlanksDefault is 1), named "text",
// and every comment, named "comment". DistributionIsExternal asks whether a
// node has ANY children, and `<Distribution> </Distribution>` has one.

type xmlNodeType int

const (
	xmlElementNode xmlNodeType = 1
	xmlTextNode    xmlNodeType = 3
	xmlPINode      xmlNodeType = 7
	xmlCommentNode xmlNodeType = 8
	xmlDTDNode     xmlNodeType = 14
)

type xmlNode struct {
	typ      xmlNodeType
	name     string
	attrs    []xml.Attr
	children *xmlNode // the first child
	next     *xmlNode
	parent   *xmlNode
}

type xmlDoc struct {
	children *xmlNode
}

// xmlParseMemory is libxml2's: a document, or nil when it is not well formed.
// The patcher hands it a C string, so the buffer ends at its first NUL.
func xmlParseMemory(mem []byte) *xmlDoc {
	if i := bytes.IndexByte(mem, 0); i >= 0 {
		mem = mem[:i]
	}
	dec := xml.NewDecoder(bytes.NewReader(mem))
	dec.Strict = true
	dec.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "iso-8859-1", "latin1", "latin-1":
			return latin1Reader{in}, nil
		}
		return nil, io.ErrUnexpectedEOF
	}
	doc := &xmlDoc{}
	var parent *xmlNode // nil: the document
	var last *xmlNode   // the last child appended at this level
	lastOf := map[*xmlNode]*xmlNode{}
	add := func(n *xmlNode) {
		n.parent = parent
		if parent == nil {
			if last == nil {
				doc.children = n
			} else {
				last.next = n
			}
			last = n
			return
		}
		if l := lastOf[parent]; l == nil {
			parent.children = n
		} else {
			l.next = n
		}
		lastOf[parent] = n
	}
	roots := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if parent == nil {
				roots++
				if roots > 1 {
					return nil // "Extra content at the end of the document"
				}
			}
			n := &xmlNode{typ: xmlElementNode, name: t.Name.Local, attrs: t.Attr}
			add(n)
			parent = n
		case xml.EndElement:
			parent = parent.parent
		case xml.CharData:
			if parent == nil {
				if len(bytes.TrimSpace(t)) != 0 {
					return nil
				}
				continue // blanks outside the root make no node
			}
			if l := lastOf[parent]; l != nil && l.typ == xmlTextNode {
				continue // adjacent text is one node
			}
			add(&xmlNode{typ: xmlTextNode, name: "text"})
		case xml.Comment:
			add(&xmlNode{typ: xmlCommentNode, name: "comment"})
		case xml.ProcInst:
			if t.Target != "xml" { // the declaration makes no node
				add(&xmlNode{typ: xmlPINode, name: t.Target})
			}
		case xml.Directive:
			add(&xmlNode{typ: xmlDTDNode, name: "dtd"})
		}
	}
	if roots == 0 {
		return nil
	}
	return doc
}

// root is xmlDocGetRootElement: the document's first element.
func (d *xmlDoc) root() *xmlNode {
	if d == nil {
		return nil
	}
	for n := d.children; n != nil; n = n.next {
		if n.typ == xmlElementNode {
			return n
		}
	}
	return nil
}

// xmlGetProp is libxml2's: the attribute's value, or nil when the element has
// no attribute of that name. Names compare case for case.
func xmlGetProp(n *xmlNode, name string) cstr {
	if n == nil || n.typ != xmlElementNode {
		return nil
	}
	for _, a := range n.attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			v := a.Value
			return &v
		}
	}
	return nil
}

type latin1Reader struct{ r io.Reader }

func (l latin1Reader) Read(p []byte) (int, error) {
	buf := make([]byte, len(p)/2+1)
	n, err := l.r.Read(buf)
	out := p[:0]
	for _, b := range buf[:n] {
		if b < 0x80 {
			out = append(out, b)
		} else {
			out = append(out, 0xC0|b>>6, 0x80|b&0x3F)
		}
	}
	return len(out), err
}
