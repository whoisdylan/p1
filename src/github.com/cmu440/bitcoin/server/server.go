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
	"math"
)

type workRequest struct {
	message bitcoin.Message
	clientID int
}

type serverInfo struct {
	s Server
	freeMiners *list.List
//	busyMiners *list.List
	clients map[int]bool
	//list of request messages that are freely available
	freeWork *list.List
	//maps miner IDs to {work request, client ID}
	takenWork map[int]workRequest
	newWorkRequest chan workRequest
	newMinerAvailable chan int
	newResultAvailable chan workRequest
	minerFreed chan int
	newError chan int
//	workFreed chan struct{}
	workInProgress map[int]bitcoin.Message
}

func main() {
	const numArgs = 2
	if len(os.Args) != numArgs {
		fmt.Println("Usage: ./server <port>")
		return
	}

	serv := new(serverInfo)
	serv.freeMiners = list.New()
//	serv.busyMiners = list.New()
	serv.clients = new(map[int]bool)
	serv.freeWork = list.New()
	serv.takenWork = new(map[int]bitcoin.Message)
	serv.newWorkRequest = make(chan workRequest)
	serv.newMinerAvailable = make(chan int)
	serv.newResultAvailable = make(chan workRequest)
	serv.newError = make(chan int)
	serv.minerFreed = make(chan int)
//	serv.workFreed = make(chan struct{})
	serv.workInProgress = make(map[int]bitcoin.Message)

	port,_ := strconv.Atoi(os.Args[1])
	params := lsp.NewParams()
	s,_ := lsp.NewServer(port, params)
	serv.s = s
	go networkHandler(serv)

	select {
	case newWorkRequest := <- serv.newWorkRequest:
		newTempMess := bitcoin.NewResult(math.MaxUint64, 0)
		serv.workInProgress[newWorkRequest.clientID] = newTempMess
		serv.clients[newWorkRequest.clientID] = true
		var workSize uint64 = 1000
		maxNonce := newWorkRequest.message.Upper
		for maxNonce >= workSize {
			minNonce := maxNonce - workSize + 1
			workMess := bitcoin.NewRequest(newWorkRequest.message.Data, minNonce, maxNonce)
			workReq := new(workRequest)
			workReq.message = workMess
			workReq.clientID = newWorkRequest.clientID
			serv.freeWork.PushBack(workReq)
			maxNonce = minNonce - 1
		}
		if maxNonce > 0 {
			workMess := bitcoin.NewRequest(newWorkRequest.message.Data, 0, maxNonce)
			workReq := new(workRequest)
			workReq.message = workMess
			workReq.clientID = newWorkRequest.clientID
			serv.freeWork.PushBack(workReq)
			maxNonce = 0
		}
	case newMiner := <- serv.newMinerAvailable:
		if serv.freeWork.Len() != 0 {
			workload := serv.freeWork.Front().Value
			serv.takenWork[workload.clientID] = workload
			workMess := bitcoin.NewRequest(workload.message.Data, workload.message.lower, workload.message.upper)
			serv.freeWork.Remove(serv.freeWork.Front())
			marshMess,_ := json.Marshal(workMess)
			serv.s.Write(newMiner, marshMess)
		} else {
			serv.freeMiners.PushBack(newMiner)
//			go freeMiner(serv)
		}
	case newResult := <-serv.newResultAvailable:
		ID := newResult.clientID
		unmarshMess := newResult.message
		hash := unmarshMess.Hash
		jobID := serv.takenWork[ID].clientID
		//			check if client disconnected
		if serv.clients[jobID] == true {
			minHash := serv.workInProgress[jobID].Hash
			if hash < minHash {
				serv.workInProgress[jobID] = unmarshMess
			}
			delete(serv.takenWork, ID)
			//check if there's any more work for this ID
			returnResult := true
			for ID,_ := range serv.takenWork {
				if ID == jobID {
					returnResult = false
				}
			}
			for ID,_ := range serv.freeWork {
				if ID == jobID {
					returnResult = false
				}
			}
			if returnResult == true {
				marshResult,_ := json.Marshal(serv.workInProgress[jobID])
				delete(serv.workInProgress, jobID)
				serv.s.Write(jobID, marshResult)
			}
		}
	case ID := <- serv.newError:
		//check if we lost a miner or client.  if client, we can ignore the results
		//if miner, reallocate its work

		//it's a miner
		if workReq,found := serv.takenWork[ID]; found == true {
			serv.freeWork.PushBack(workReq)
			delete(serv.takenWork,ID)
		} else {
			serv.clients[ID] = false
		}
//	case <-serv.workFreed:
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

		if readError != nil {
			serv.newErrorAvailable <- ID
		}
		var unmarshMess bitcoin.Message
		json.Unmarshal(marshMess,&unmarshMess)
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
			newResult := new(workRequest)
			newResult.clientID = ID
			newResult.message = unmarshMess
			serv.newResultAvailable <- newResult
			serv.newMinerAvailable <- ID
		}
	}
}

func freeMiner(serv *serverInfo, minerID int) {
	serv.minerFreed <- minerID
}
