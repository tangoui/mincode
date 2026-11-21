package dbimport

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

const (
	maxXMLDepth = 64
	maxXMLNodes = 200000
)

func decodeXML(data []byte, target any) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	decoder.CharsetReader = passThroughCharset
	return decoder.Decode(target)
}

// passThroughCharset accepts UTF-8 compatible declarations ("utf-8",
// "UTF8", "us-ascii"); anything else is rejected rather than misread.
func passThroughCharset(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.ReplaceAll(label, "-", "")) {
	case "utf8", "usascii", "ascii":
		return input, nil
	}
	return nil, errors.New("unsupported charset")
}

// xmlNode is a minimal DOM used by the JetBrains and Workbench importers.
type xmlNode struct {
	Name     string
	Attrs    map[string]string
	Text     string
	Children []*xmlNode
}

func (n *xmlNode) attr(name string) string {
	return n.Attrs[strings.ToLower(name)]
}

// child returns the first direct child with the given name.
func (n *xmlNode) child(name string) *xmlNode {
	for _, child := range n.Children {
		if strings.EqualFold(child.Name, name) {
			return child
		}
	}
	return nil
}

func (n *xmlNode) childText(name string) string {
	if child := n.child(name); child != nil {
		return strings.TrimSpace(child.Text)
	}
	return ""
}

// walk visits every descendant (depth first).
func (n *xmlNode) walk(visit func(*xmlNode)) {
	for _, child := range n.Children {
		visit(child)
		child.walk(visit)
	}
}

// parseXMLTree parses a document into a bounded tree.
func parseXMLTree(data []byte) (*xmlNode, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	decoder.CharsetReader = passThroughCharset
	root := &xmlNode{Name: "#document"}
	stack := []*xmlNode{root}
	nodes := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			nodes++
			if nodes > maxXMLNodes || len(stack) > maxXMLDepth {
				return nil, errors.New("xml: document too large")
			}
			node := &xmlNode{Name: typed.Name.Local, Attrs: make(map[string]string, len(typed.Attr))}
			for _, attr := range typed.Attr {
				node.Attrs[strings.ToLower(attr.Name.Local)] = attr.Value
			}
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, node)
			stack = append(stack, node)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			current := stack[len(stack)-1]
			if len(current.Text) < maxFieldBytes {
				current.Text += string(typed)
			}
		}
	}
	if len(root.Children) == 0 {
		return nil, errors.New("xml: empty document")
	}
	return root, nil
}
