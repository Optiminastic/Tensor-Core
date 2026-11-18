package httpapi

import "testing"

func TestTheTemplateSourceCacheKeepsEachVersionAndStaysBounded(t *testing.T) {
	c := &templateSourceCache{entries: map[string][]byte{}}
	if _, ok := c.get("templates/rose_plate/v1.scad"); ok {
		t.Fatal("an empty cache answered")
	}
	c.put("templates/rose_plate/v1.scad", []byte("NAME = \"A\";"))
	if src, ok := c.get("templates/rose_plate/v1.scad"); !ok || string(src) != "NAME = \"A\";" {
		t.Fatalf("got %q, %v", src, ok)
	}
	// A replaced file is a new object under a new key; the old one is untouched.
	c.put("templates/rose_plate/v2.scad", []byte("NAME = \"B\";"))
	if src, _ := c.get("templates/rose_plate/v2.scad"); string(src) != "NAME = \"B\";" {
		t.Errorf("the new version read %q", src)
	}
	// Past the size cap the cache starts again rather than growing.
	big := make([]byte, templateSourceCacheMaxBytes-5)
	c.put("templates/huge/v1.scad", big)
	if c.bytes > templateSourceCacheMaxBytes {
		t.Errorf("cache holds %d bytes, cap %d", c.bytes, templateSourceCacheMaxBytes)
	}
	if _, ok := c.get("templates/huge/v1.scad"); !ok {
		t.Error("the file that triggered the reset was not kept")
	}
}
