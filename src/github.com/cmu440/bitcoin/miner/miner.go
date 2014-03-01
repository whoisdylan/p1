package main

import (
	"encoding/json"
	"fmt"
	"github.com/cmu440/bitcoin"
	"github.com/cmu440/lsp"
	"math"
	"os"
)

func main() {
	const numArgs = 2
	if len(os.Args) != numArgs {
		fmt.Println("Usage: ./miner <hostport>")
		return
	}

	params := lsp.NewParams()
	client, _ := lsp.NewClient(os.Args[1], params)
	join := bitcoin.NewJoin()
	marshMess, _ := json.Marshal(join)
	//	fmt.Println("addr=",os.Args[1])
	//	fmt.Println("msg=",os.Args[2])
	//	fmt.Println("maxNonce=",uint64(maxNonce))
	writeError := client.Write(marshMess)
	if writeError != nil {
		//		fmt.Println(writeError)
		return
	}
	for {
		marshResult, readError := client.Read()
		if readError != nil {
			//			fmt.Println(readError)
			return
		}
		var unmarshResult bitcoin.Message
		json.Unmarshal(marshResult, &unmarshResult)
		data := unmarshResult.Data
		minNonce := unmarshResult.Lower
		maxNonce := unmarshResult.Upper

		var minHash uint64 = math.MaxUint64
		//	minHash := math.MaxUint64
		var nonce uint64 = 0
		for i := minNonce; i < maxNonce; i++ {
			var currHash uint64 = bitcoin.Hash(data, i)
			if currHash < minHash {
				minHash = currHash
				nonce = i
			}
		}
		resultMess := bitcoin.NewResult(minHash, nonce)
		marshResultMess, _ := json.Marshal(resultMess)
		writeErr := client.Write(marshResultMess)
		if writeErr != nil {
			//			fmt.Println(writeErr)
			return
		}
	}
	client.Close()
	return
}
