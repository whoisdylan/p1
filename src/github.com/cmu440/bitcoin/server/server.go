package main

import (
	//	"fmt"
	"container/list"
	"encoding/json"
	"github.com/cmu440/bitcoin"
	"github.com/cmu440/lsp"
	"math"
	"os"
	"strconv"
	//	"log"
)

type workRequest struct {
	message  bitcoin.Message
	clientID int
}

type serverInfo struct {
	s          lsp.Server
	freeMiners *list.List
	//	busyMiners *list.List
	clients map[int]bool
	//list of request messages that are freely available
	freeWork *list.List
	//maps miner IDs to {work request, client ID}
	takenWork          map[int]workRequest
	newWorkRequest     chan workRequest
	newMinerAvailable  chan int
	newResultAvailable chan workRequest
	minerFreed         chan int
	newError           chan int
	//	workFreed chan struct{}
	workInProgress map[int]bitcoin.Message
	//	log *log.Logger
}

const (
	name = "log.txt"
	flag = os.O_RDWR | os.O_CREATE
	perm = os.FileMode(0666)
)

func main() {
	const numArgs = 2
	if len(os.Args) != numArgs {
		//		fmt.Println("Usage: ./server <port>")
		return
	}

	serv := new(serverInfo)
	serv.freeMiners = list.New()
	//	serv.busyMiners = list.New()
	serv.clients = make(map[int]bool)
	serv.freeWork = list.New()
	serv.takenWork = make(map[int]workRequest)
	serv.newWorkRequest = make(chan workRequest)
	serv.newMinerAvailable = make(chan int)
	serv.newResultAvailable = make(chan workRequest)
	serv.newError = make(chan int)
	serv.minerFreed = make(chan int)
	//	serv.workFreed = make(chan struct{})
	serv.workInProgress = make(map[int]bitcoin.Message)

	//logger stuff
	//	file, err := os.OpenFile(name, flag, perm)
	//	if err != nil {
	//		return
	//	}
	////	serv.log = log.New(file, "", log.Lshortfile|log.Lmicroseconds)
	//	defer file.Close()

	port, _ := strconv.Atoi(os.Args[1])
	params := lsp.NewParams()
	s, _ := lsp.NewServer(port, params)
	serv.s = s
	go networkHandler(serv)

	for {
		select {
		case newWorkRequest := <-serv.newWorkRequest:
			//			serv.log.Println("adding new work to the work queue")
			//			fmt.Println("adding new work to the work queue")
			newTempMess := bitcoin.NewResult(math.MaxUint64, 0)
			serv.workInProgress[newWorkRequest.clientID] = *newTempMess
			serv.clients[newWorkRequest.clientID] = true
			//			fmt.Println("client",newWorkRequest.clientID," set in client map")
			var workSize uint64 = 1000
			maxNonce := newWorkRequest.message.Upper
			for maxNonce >= workSize {
				minNonce := maxNonce - workSize + 1
				workMess := bitcoin.NewRequest(newWorkRequest.message.Data, minNonce, maxNonce)
				workReq := new(workRequest)
				workReq.message = *workMess
				workReq.clientID = newWorkRequest.clientID
				serv.freeWork.PushBack(*workReq)
				maxNonce = minNonce - 1
			}
			if maxNonce > 0 {
				workMess := bitcoin.NewRequest(newWorkRequest.message.Data, 0, maxNonce)
				workReq := new(workRequest)
				workReq.message = *workMess
				workReq.clientID = newWorkRequest.clientID
				serv.freeWork.PushBack(*workReq)
				maxNonce = 0
			}
			//			serv.log.Println("done adding new work to the work queue")
			//			fmt.Println("done adding new work to the work queue")
		case newMiner := <-serv.newMinerAvailable:
			//			serv.log.Println("master dealing with new miner")
			//			fmt.Println("master dealing with new miner")
			if serv.freeWork.Len() != 0 {
				//				serv.log.Println("assigning work to new miner")
				workload := serv.freeWork.Front().Value.(workRequest)
				//				fmt.Println("assigning work to new miner from client",workload.clientID)
				serv.takenWork[newMiner] = workload
				workMess := bitcoin.NewRequest(workload.message.Data, workload.message.Lower, workload.message.Upper)
				serv.freeWork.Remove(serv.freeWork.Front())
				marshMess, _ := json.Marshal(workMess)
				serv.s.Write(newMiner, marshMess)
			} else {
				//				serv.log.Println("no work available, adding new miner to free miner queue")
				//				fmt.Println("no work available, adding new miner to free miner queue")
				serv.freeMiners.PushBack(newMiner)
				//			go freeMiner(serv)
			}
		case newResult := <-serv.newResultAvailable:
			ID := newResult.clientID
			unmarshMess := newResult.message
			hash := unmarshMess.Hash
			jobID := serv.takenWork[ID].clientID
			//			fmt.Println("New result available from work for client",serv.takenWork[ID].clientID)
			//			check if client disconnected
			//			fmt.Println("checking for client",jobID,"result:",serv.clients[jobID])
			if serv.clients[jobID] == true {
				//				fmt.Println("client still connected, checking if new min hash found")
				minHash := serv.workInProgress[jobID].Hash
				if hash < minHash {
					//					fmt.Println("new min hash found")
					serv.workInProgress[jobID] = unmarshMess
				}
				delete(serv.takenWork, ID)
				//check if there's any more work for this ID
				returnResult := true
				for ID, _ := range serv.takenWork {
					if ID == jobID {
						//						fmt.Println("still more taken work to do with clientID",jobID)
						returnResult = false
						break
					}
				}
				for mess := serv.freeWork.Front(); mess != nil; mess = mess.Next() {
					if mess.Value.(workRequest).clientID == jobID {
						//						fmt.Println("Still more free work to do with clientID", jobID)
						returnResult = false
						break
					}
				}
				if returnResult == true {
					//					fmt.Println("returning final result to client", jobID)
					marshResult, _ := json.Marshal(serv.workInProgress[jobID])
					delete(serv.workInProgress, jobID)
					serv.s.Write(jobID, marshResult)
				}
			}
		case ID := <-serv.newError:
			//check if we lost a miner or client.  if client, we can ignore the results
			//if miner, reallocate its work

			//it's a miner
			//			fmt.Println("Disconnection detected")
			if workReq, found := serv.takenWork[ID]; found == true {
				//				fmt.Println("Miner disconnected!")
				serv.freeWork.PushBack(workReq)
				delete(serv.takenWork, ID)
			} else {
				//				fmt.Println("Client disconnected!")
				serv.clients[ID] = false
			}
			//	case <-serv.workFreed:
			//	case minerID := <-serv.minerFreed:
			//		if serv.freeWork.Len() != 0 {
			//			workload := serv.freeWork.Front().Value.(workRequest)
			//			workMess := bitcoin.NewRequest(workload.Data, workload.Lower, workload.Upper)
			//			serv.freeWork.Remove(serv.freeWork.Front())
			//			marshMess,_ := json.Marshal(workMess)
			//			serv.s.Write(minerID, marshMess)
			//		}
		}
	}
}

func networkHandler(serv *serverInfo) {
	for {
		ID, marshMess, readError := serv.s.Read()

		if readError != nil {
			serv.newError <- ID
		}
		var unmarshMess bitcoin.Message
		json.Unmarshal(marshMess, &unmarshMess)
		switch unmarshMess.Type {
		//new miner joining
		case bitcoin.Join:
			//			serv.log.Println("NEW MINER!")
			//			fmt.Println("NEW MINER!")
			serv.newMinerAvailable <- ID
		//new client request
		case bitcoin.Request:
			//			serv.log.Println("NEW CLIENT!")
			//			fmt.Println("NEW CLIENT!")
			newWorkRequest := new(workRequest)
			newWorkRequest.clientID = ID
			newWorkRequest.message = unmarshMess
			serv.newWorkRequest <- *newWorkRequest
		//miner result
		case bitcoin.Result:
			newResult := new(workRequest)
			newResult.clientID = ID
			newResult.message = unmarshMess
			serv.newResultAvailable <- *newResult
			serv.newMinerAvailable <- ID
		}
	}
}

//func freeMiner(serv *serverInfo, minerID int) {
//	serv.minerFreed <- minerID
//}
