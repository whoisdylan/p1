package main

import (
	"fmt"
	"os"
	"github.com/cmu440/lsp"
	"strconv"
	"encoding/json"
	"github.com/cmu440/bitcoin"
	"container/list"
	"github.com/cmu440/bitcoin/server"
)

type workRequest struct {
	message bitcoin.Message
	clientID int
}

type workResults struct {
	hash
}

type serverInfo struct {
	s Server
	freeMiners *list.List
	busyMiners *list.List
	clients *list.List
	//list of request messages that are freely available
	freeWork *list.List
	//maps miner IDs to {work request, client ID}
	takenWork map[int]workRequest
	newWorkRequest chan workRequest
	newMinerAvailable chan int
	minerFreed chan int
	workFreed chan struct{}
	workInProgress map[int]workResults
}

func main() {
	const numArgs = 2
	if len(os.Args) != numArgs {
		fmt.Println("Usage: ./server <port>")
		return
	}

	serv := new(serverInfo)
	serv.freeMiners = list.New()
	serv.busyMiners = list.New()
	serv.clients = list.New()
	serv.freeWork = list.New()
	serv.takenWork = new(map[int]bitcoin.Message)
	serv.newWorkRequest = make(chan workRequest)
	serv.newMinerAvailable = make(chan int)
	serv.minerFreed = make(chan int)
	serv.workFreed = make(chan struct{})

	port,_ := strconv.Atoi(os.Args[1])
	params := lsp.NewParams()
	s,_ := lsp.NewServer(port, params)
	serv.s = s
	go networkHandler(serv)

	select {
	case newWorkRequest := <- serv.newWorkRequest:
		var workSize uint64 = 1000
		maxNonce := newWorkRequest.message.Upper
		for maxNonce >= workSize {
			minNonce := maxNonce - workSize + 1
			workMess := bitcoin.NewRequest(newWorkRequest.message.Data, minNonce, maxNonce)
			serv.freeWork.PushBack(workMess)
			maxNonce = minNonce - 1
		}
		if maxNonce > 0 {
			workMess := bitcoin.NewRequest(newWorkRequest.message.Data, 0, maxNonce)
			serv.freeWork.PushBack(workMess)
			maxNonce = 0
		}
	case newMiner := <- serv.newMinerAvailable:
		if serv.freeWork.Len() != 0 {
			workload := serv.freeWork.Front().Value
			workMess := bitcoin.NewRequest(workload.Data, workload.lower, workload.upper)
			serv.freeWork.Remove(serv.freeWork.Front())
			marshMess,_ := json.Marshal(workMess)
			serv.s.Write(newMiner, marshMess)
		} else {
			serv.freeMiners.PushBack(newMiner)
//			go freeMiner(serv)
		}
	case <-serv.workFreed:
//	case minerID := <-serv.minerFreed:
//		if serv.freeWork.Len() != 0 {
//			workload := serv.freeWork.Front().Value
//			workMess := bitcoin.NewRequest(workload.Data, workload.lower, workload.upper)
//			serv.freeWork.Remove(serv.freeWork.Front())
//			marshMess,_ := json.Marshal(workMess)
//			serv.s.Write(minerID, marshMess)
//		}
	}

}

func networkHandler(serv *serverInfo) {
	for {
		ID, marshMess, readError := serv.s.Read()
		var unmarshMess bitcoin.Message
		json.Unmarshal(marshMess,&unmarshMess)
		if readError != nil {
			//check if we lost a miner or client.  if client, we can ignore the results
			//if miner, reallocate its work

		}
		switch unmarshMess.Type {
		//new miner joining
		case bitcoin.Join:
			serv.newMinerAvailable <- ID
		//new client request
		case bitcoin.Request:
			newWorkRequest := new(workRequest)
			newWorkRequest.clientID = ID
			newWorkRequest.message = unmarshMess
			serv.newWorkRequest <- newWorkRequest
		//miner result
		case bitcoin.Result:

		}
	}
}

func freeMiner(serv *serverInfo, minerID int) {
	serv.minerFreed <- minerID
}
