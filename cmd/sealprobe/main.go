// sealprobe resolves a model DictID through the *client's* ESF stack, the same
// call the renderer makes, and reports the geometry it gets back. This is the
// check that the wire ModelID the server sends is the ID the client can load.
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/DabDavis/eqoa-esf-tools/pkg/esf"
)

func main() {
	path := flag.String("esf", "", "CHAR.ESF to read")
	id := flag.Int64("id", 0, "model DictID as the wire carries it")
	flag.Parse()

	f, err := esf.Open(*path)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	obj, err := f.FindObject(int32(uint32(*id)))
	if err != nil {
		log.Fatalf("FindObject(0x%08X): %v", uint32(*id), err)
	}
	if obj == nil {
		log.Fatalf("FindObject(0x%08X): not in dictionary", uint32(*id))
	}
	fmt.Printf("0x%08X resolved: %T\n", uint32(*id), obj)
	cs, ok := obj.(*esf.CSprite)
	if !ok {
		return
	}
	bones := 0
	if cs.Hierarchy != nil {
		bones = len(cs.Hierarchy.Nodes)
	}
	fmt.Printf("  header DictID=0x%08X SkelType=%d Race=%d Sex=%d DefaultScale=%.4f bones=%d anims=%d\n",
		uint32(cs.DictID), cs.SkelType, cs.Race, cs.Sex, cs.DefaultScale, bones, len(cs.Animations))
}
