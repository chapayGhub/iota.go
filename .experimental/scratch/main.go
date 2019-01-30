package main

import (
	"bytes"
	"fmt"
	"github.com/iotaledger/iota.go/guards/validators"
	"github.com/iotaledger/iota.go/trinary"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

const resp = `{"state":false,"info":"tails are not solid (missing a referenced tx): QLEYGYDJLMS9VJJHBMDPNIFWXKLZXRSR9FFBCMPIHNCRZKGVKIGZDQF9GJIXLFNJJRHCRZQNSXDH99999"}`

func main() {
	addr := "OHXRRYM9XAOOXBLWIFWSMMDUYSRVRK9RWHPMNRFDTKUYZWENMPGHPHKBECU9HRJMAYSQM9JRAS9CTGWBNPQBPIMGSX"
	fmt.Println(string(addr[80]))
	trits := trinary.MustTrytesToTrits(string(addr[80]))
	fmt.Println(trits)
	err := validators.Validate(validators.ValidateAddresses(true, addr))
	must(err)
}

func blil() {
	fmt.Println("GOGIRPOILFYXCODIGHKWLWLD9L9JF9ASERSLEUTNKFNCLLQEXGW9KTLELMSIYNJIEIZHLGLVSGUHZ9JOY")
	x := [243]byte{}
	fmt.Println(x)
}

func lol() {
	respBytes := []byte(resp)
	cleaned := sliceOutInfoField(respBytes)
	fmt.Print(string(cleaned))
	fmt.Println(string(respBytes))
}

const closingCurlyBraceAscii = 125

var infoKey = [7]byte{34, 105, 110, 102, 111, 34, 58}

// slices out a byte slice without the info field of check consistency calls.
// querying multiple nodes will lead to different info messages when the state
// is false of the responses.
func sliceOutInfoField(data []byte) []byte {
	infoIndex := bytes.LastIndex(data, infoKey[:])
	if infoIndex == -1 {
		return data
	}
	c := make([]byte, len(data))
	copy(c, data)
	return append(c[:infoIndex-1], closingCurlyBraceAscii)
}
