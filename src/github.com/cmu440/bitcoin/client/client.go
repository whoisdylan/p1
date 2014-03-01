package main

import (
	"encoding/json"
	"fmt"
	"github.com/cmu440/bitcoin"
	"github.com/cmu440/lsp"
	"os"
	"strconv"
	//	"hash"
)

//os.Args = nt host:port message maxNonce
func main() {
	const numArgs = 4
	if len(os.Args) != numArgs {
		fmt.Println("Usage: ./client <hostport> <message> <maxNonce>")
		return
	}
	params := lsp.NewParams()
	client, _ := lsp.NewClient(os.Args[1], params)
	maxNonce, _ := strconv.Atoi(os.Args[3])
	request := bitcoin.NewRequest(os.Args[2], 0, uint64(maxNonce))
	marshMess, _ := json.Marshal(request)
	//	fmt.Println("addr=",os.Args[1])
	//	fmt.Println("msg=",os.Args[2])
	//	fmt.Println("maxNonce=",uint64(maxNonce))
	writeError := client.Write(marshMess)
	if writeError != nil {
		//		fmt.Println(writeError)
		return
	}
	marshResult, readError := client.Read()
	if readError != nil {
		printDisconnected()
	} else {
		var unmarshResult bitcoin.Message
		json.Unmarshal(marshResult, &unmarshResult)
		hashResult := strconv.FormatUint(unmarshResult.Hash, 10)
		nonceResult := strconv.FormatUint(unmarshResult.Nonce, 10)
		//		hashResult := fmt.Sprintf("%d",unmarshResult.Hash)
		//		nonceResult := fmt.Sprintf("%d",unmarshResult.Nonce)
		printResult(hashResult, nonceResult)
	}
	client.Close()
	return
}

// printResult prints the final result to stdout.
func printResult(hash, nonce string) {
	fmt.Println("Result", hash, nonce)
}

// printDisconnected prints a disconnected message to stdout.
func printDisconnected() {
	fmt.Println("Disconnected")
}
