package ops

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// Plain-text extraction from Word (.docx) and OpenDocument (.odt) files
// without any engine: both are zip packages with the body in one XML part.
// Paragraphs become lines, table cells are separated by tabs and rows by
// line breaks. Deleted tracked changes, field codes and comments are left out.

const maxOfficeXML = 256 << 20

func init() {
	register(Handler{Name: "doc.text", Run: runOfficeText})
}

func runOfficeText(ctx context.Context, job Job) error {
	zr, err := zip.OpenReader(job.Input())
	if err != nil {
		return Userf("%s isn't a readable Word or OpenDocument file", job.Name(0))
	}
	defer zr.Close()
	var text string
	switch {
	case zipHas(&zr.Reader, "word/document.xml"):
		text, err = officeXMLText(&zr.Reader, "word/document.xml", docxRules)
	case zipHas(&zr.Reader, "content.xml"):
		text, err = officeXMLText(&zr.Reader, "content.xml", odtRules)
	default:
		return Userf("%s has no document body", job.Name(0))
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return os.WriteFile(job.Output, []byte(text), 0o644)
}

// OfficeText returns the plain text of a .docx or .odt file.
func OfficeText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	switch {
	case zipHas(&zr.Reader, "word/document.xml"):
		return officeXMLText(&zr.Reader, "word/document.xml", docxRules)
	case zipHas(&zr.Reader, "content.xml"):
		return officeXMLText(&zr.Reader, "content.xml", odtRules)
	}
	return "", Userf("no document body")
}

func zipHas(z *zip.Reader, name string) bool {
	for _, f := range z.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

// officeRules says how a format's XML maps onto text.
type officeRules struct {
	text  func(xml.Name) bool           // elements whose character data is text
	skip  func(xml.Name) bool           // subtrees ignored entirely
	start func(xml.StartElement) string // text emitted when an element opens
	end   func(xml.Name) string         // text emitted when an element closes
}

const (
	nsW      = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsText   = "urn:oasis:names:tc:opendocument:xmlns:text:1.0"
	nsTable  = "urn:oasis:names:tc:opendocument:xmlns:table:1.0"
	nsOffice = "urn:oasis:names:tc:opendocument:xmlns:office:1.0"
)

var docxRules = officeRules{
	text: func(n xml.Name) bool { return n.Space == nsW && n.Local == "t" },
	skip: func(n xml.Name) bool {
		return n.Space == nsW && (n.Local == "delText" || n.Local == "instrText" || n.Local == "del")
	},
	start: func(e xml.StartElement) string {
		if e.Name.Space != nsW {
			return ""
		}
		switch e.Name.Local {
		case "tab":
			return "\t"
		case "br", "cr":
			return "\n"
		case "noBreakHyphen":
			return "-"
		}
		return ""
	},
	end: func(n xml.Name) string {
		if n.Space != nsW {
			return ""
		}
		switch n.Local {
		case "p":
			return "\n"
		case "tc":
			return "\t"
		case "tr":
			return "\n"
		}
		return ""
	},
}

var odtRules = officeRules{
	text: func(n xml.Name) bool {
		return n.Space == nsText && (n.Local == "p" || n.Local == "h" || n.Local == "span" || n.Local == "a")
	},
	skip: func(n xml.Name) bool {
		return n.Space == nsOffice && n.Local == "annotation" ||
			n.Space == nsText && (n.Local == "tracked-changes" || n.Local == "note-citation")
	},
	start: func(e xml.StartElement) string {
		if e.Name.Space != nsText {
			return ""
		}
		switch e.Name.Local {
		case "tab":
			return "\t"
		case "line-break":
			return "\n"
		case "s":
			n := 1
			for _, a := range e.Attr {
				if a.Name.Local == "c" {
					if v, err := strconv.Atoi(a.Value); err == nil && v > 0 && v < 1000 {
						n = v
					}
				}
			}
			return strings.Repeat(" ", n)
		}
		return ""
	},
	end: func(n xml.Name) string {
		switch {
		case n.Space == nsText && (n.Local == "p" || n.Local == "h"):
			return "\n"
		case n.Space == nsTable && n.Local == "table-cell":
			return "\t"
		case n.Space == nsTable && n.Local == "table-row":
			return "\n"
		}
		return ""
	},
}

func officeXMLText(z *zip.Reader, part string, r officeRules) (string, error) {
	f, err := z.Open(part)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dec := xml.NewDecoder(io.LimitReader(f, maxOfficeXML))
	dec.Strict = false
	var b []byte
	var stack []bool // whether each open element collects text
	skipDepth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", Userf("This document's text couldn't be read: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skipDepth > 0 || r.skip(t.Name) {
				skipDepth++
				continue
			}
			stack = append(stack, r.text(t.Name))
			b = append(b, r.start(t)...)
		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			s := r.end(t.Name)
			if s == "\t" {
				// End of a table cell: its last paragraph's line break
				// gives way to the cell separator.
				for len(b) > 0 && b[len(b)-1] == '\n' {
					b = b[:len(b)-1]
				}
			}
			b = append(b, s...)
		case xml.CharData:
			if skipDepth == 0 && len(stack) > 0 && stack[len(stack)-1] {
				b = append(b, t...)
			}
		}
	}
	// A row ends "cell\tcell\t\n": drop the tab before each line break.
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n", nil
}
