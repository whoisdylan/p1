// Contains the implementation of a LSP server.

package lsp

import (
	"container/list"
	"encoding/json"
	"errors"
	"github.com/cmu440/lspnet"
	"strconv"
	"time"
)

type messageWithAddr struct {
	message Message
	addr    *lspnet.UDPAddr
}

type messageWithID struct {
	message Message
	connID  int
}

type clientConn struct {
	clientAddr          *lspnet.UDPAddr
	connID              int
	currSeqNum          int //seqNum counter for Write
	didReceiveFirstData bool

	receivedMessages map[int]Message

	//sliding window things
	//window for received messages
	windowRangeMin int
	windowRangeMax int
	//window for sending messages
	windowWriteRangeMin int
	windowWriteRangeMax int

	//write things
	receivedWriteMessages map[int]Message
	prevWriteMessages     map[int]bool //for checking if write messages were previously ackd

	messagesToWrite *list.List

	epochCount int

	nextBufferMessageNum int
}

type server struct {
	params *Params

	//epochHandler things
	//epochCount           chan int
	//didRequestEpochCount chanSignal
	resetEpochCount chan int
	epochDidOccur   chan int

	//networkHandler things
	writeMess chan Message

	//readIn things
	//readInMess           chan Message
	//readInMessSenderAddr chan *lspnet.UDPAddr
	readInMessWithAddr chan messageWithAddr
	messagesToReadIn   chan messageWithID
	messagesToReadOut  chan messageWithID

	newWriteMess chan Message
	//newReadMess     chan Message
	//newReadMessAddr chan *lspnet.UDPAddr
	newReadMessWithAddr chan messageWithAddr
	//doneWithNewWriteMess chanSignal
	//doneWithNewReadMess chanSignal

	//message things
	//messagesToWriteIn  chan Message
	//messagesToWriteOut chan Message
	doneFlushingReadOut chanSignal

	//client things
	currConnID    int
	clientMap     map[int]*clientConn
	clientAddrMap map[string]*clientConn //stores true if already connected, false if ackd and received first data

	clientsToCloseIn  chan int
	clientsToCloseOut chan int

	conn      *lspnet.UDPConn
	closeConn chan *clientConn
	shutdown  chanSignal
}

// NewServer creates, initiates, and returns a new server. This function should
// NOT block. Instead, it should spawn one or more goroutines (to handle things
// like accepting incoming client connections, triggering epoch events at
// fixed intervals, synchronizing events using a for-select loop like you saw in
// project 0, etc.) and immediately return. It should return a non-nil error if
// there was an error resolving or listening on the specified port number.
func NewServer(port int, params *Params) (Server, error) {
	s := new(server)
	s.params = params
	s.epochDidOccur = make(chan int)
	s.resetEpochCount = make(chan int)
	s.clientMap = make(map[int]*clientConn)
	s.clientAddrMap = make(map[string]*clientConn)
	s.messagesToReadIn = make(chan messageWithID)
	s.messagesToReadOut = make(chan messageWithID)
	//s.readInMess = make(chan Message)
	//s.readInMessSenderAddr = make(chan *lspnet.UDPAddr)
	s.readInMessWithAddr = make(chan messageWithAddr)
	s.doneFlushingReadOut = make(chanSignal)
	s.shutdown = make(chanSignal)
	s.currConnID = 1
	//s.doneWithNewWriteMess = make(chanSignal)
	s.newWriteMess = make(chan Message)
	//s.doneWithNewReadMess = make(chanSignal)
	//s.newReadMess = make(chan Message)
	//s.newReadMessAddr = make(chan *lspnet.UDPAddr)
	s.newReadMessWithAddr = make(chan messageWithAddr)
	s.closeConn = make(chan *clientConn)
	s.clientsToCloseIn = make(chan int)
	s.clientsToCloseOut = make(chan int)

	//bring the server online
	addr, resolveError := lspnet.ResolveUDPAddr("udp", lspnet.JoinHostPort("localhost", strconv.Itoa(port)))
	if resolveError != nil {
		return nil, resolveError
	}

	var connError error
	s.conn, connError = lspnet.ListenUDP("udp", addr)
	if connError != nil {
		return nil, connError
	}

	go handleBufferS(s.messagesToReadIn, s.messagesToReadOut, s)
	go handleBufferS2(s.clientsToCloseIn, s.clientsToCloseOut, s)
	//go epochHandlerS(s, params.EpochLimit, params.EpochMillis)
	go masterS(s)

	logv.Printf("New server created and listening for connections\n")
	return s, nil
}

func (s *server) Read() (int, []byte, error) {
	logv.Printf("Server Read called\n")
	select {
	case <-s.shutdown:
		return 0, nil, errors.New("Cannot Read, connection closed")
	case newMessWithID := <-s.messagesToReadOut:
		if _, found := s.clientMap[newMessWithID.connID]; found == false {
			logv.Printf("Server Read Error, connID %d not found\n", newMessWithID.connID)
			return newMessWithID.connID, nil, errors.New("Cannot Read, connection closed")
		}
		logv.Printf("Server read returning seqNum=%d to connID=%d\n", newMessWithID.message.SeqNum, newMessWithID.connID)
		return newMessWithID.connID, newMessWithID.message.Payload, nil
	case clientToClose := <-s.clientsToCloseOut:
		logv.Printf("Server Read closing connID %d\n", clientToClose)
		return clientToClose, nil, errors.New("Cannot Read, connection closed")

	}
	return -1, nil, errors.New("Read error")
}

func (s *server) Write(connID int, payload []byte) error {
	clientConn, found := s.clientMap[connID]
	if found == false {
		return errors.New("Cannot write to specified connection ID, not connected")
	}
	select {
	case <-s.shutdown:
		return errors.New("Could not write, connection closed")
	default:
		logv.Printf("Write is creating new data message\n")
		clientConn.currSeqNum += 1
		newWriteMess := *NewData(connID, clientConn.currSeqNum-1, payload)
		s.newWriteMess <- newWriteMess

		/*if newWriteMess.SeqNum >= clientConn.windowWriteRangeMin && newWriteMess.SeqNum <= clientConn.windowWriteRangeMax {
			clientConn.receivedWriteMessages[newWriteMess.SeqNum] = newWriteMess
			writeMessS(s, clientConn, newWriteMess)
		} else {
			clientConn.messagesToWrite.PushBack(newWriteMess)
			//c.messagesToWriteIn <- newWriteMess
		}
		clientConn.currSeqNum += 1
		s.doneWithNewWriteMess  <- struct{}{}*/
	}
	return nil
}

func (s *server) CloseConn(connID int) error {
	return errors.New("Not yet implemented")
	clientConn, found := s.clientMap[connID]
	if found == false {
		return errors.New("Can't close connection, connection ID doesn't exit")
	}
	s.closeConn <- clientConn
	return nil
}

func (s *server) Close() error {
	logv.Printf("Server Close called\n")
	return nil
	for {
		select {
		case <-s.shutdown:
			return nil
			//block until all pending writes to clients are ackd
			/*default:
			for _, currClient := range s.clientMap {
				for currClient.messagesToWrite.Len() != 0 || len(currClient.receivedWriteMessages) != 0 {
				}
			}
			close(s.shutdown)
			return nil*/
		}
	}
	return errors.New("not yet implemented")
}

func masterS(s *server) error {
	go readInS(s)
	//go networkHandlerS(s)
	epochFired := time.Tick(time.Duration(s.params.EpochMillis) * time.Millisecond)
	for {
		select {
		case <-epochFired:
			for _, clientConn := range s.clientAddrMap {
				clientConn.epochCount += 1
				logv.Printf("Server epoch fired and handled by master")
				//c.didRequestEpochCount <- struct{}{}
				//epochCount := <-c.epochCount
				if clientConn.epochCount >= s.params.EpochLimit {
					logv.Printf("Server's master exceeded epoch limit for connID %d\n", clientConn.connID)
					//if epoch limit exceeded, let client Read all extra data
					//					for _, clientConn := range s.clientMap {
					//						updateReadBufferS(s, clientConn)
					//					}
					//updateReadBufferS(s, clientConn)
					//remove the client connection from map so Read can check and return err
					delete(s.clientMap, clientConn.connID)
					delete(s.clientAddrMap, clientConn.clientAddr.String())
					s.clientsToCloseIn <- clientConn.connID
					//close(s.messagesToReadIn)
					//<-s.doneFlushingReadOut
					//close(s.shutdown)
					//return errors.New("Server epoch limit reached in master go routine")
				}
				//for all clients that are connected but haven't received data yet, send more acks
				if clientConn.didReceiveFirstData == false {
					logv.Printf("Server acking dataless connID= %d\n", clientConn.connID)
					sendAckS(s, 0, clientConn)
				}
				logv.Printf("Server reacking received messages connID=%d\n", clientConn.connID)
				for _, message := range clientConn.receivedMessages {
					logv.Printf("Reack SeqNum=%d\n", message.SeqNum)
					sendAckS(s, message.SeqNum, clientConn)
				}
				logv.Printf("Server resending nonack'd messages connID=%d\n", clientConn.connID)
				for _, message := range clientConn.receivedWriteMessages {
					logv.Printf("Resend SeqNum=%d\n", message.SeqNum)
					writeMessS(s, clientConn, message)
				}
			}
		case connIDToReset := <-s.resetEpochCount:
			clientConn := s.clientMap[connIDToReset]
			clientConn.epochCount = 0
		case newWriteMess := <-s.newWriteMess:
			clientConn := s.clientMap[newWriteMess.ConnID]
			if newWriteMess.SeqNum >= clientConn.windowWriteRangeMin && newWriteMess.SeqNum <= clientConn.windowWriteRangeMax {
				logv.Printf("Server adding new write message to window seqNum=%d\n", newWriteMess.SeqNum)
				clientConn.receivedWriteMessages[newWriteMess.SeqNum] = newWriteMess
				writeMessS(s, clientConn, newWriteMess)
			} else {
				logv.Printf("Server appending write message to buffer seqNum=%d\n", newWriteMess.SeqNum)
				clientConn.messagesToWrite.PushBack(newWriteMess)
				//c.messagesToWriteIn <- newWriteMess
			}
		case newMessWithAddr := <-s.readInMessWithAddr:
			//newMessSenderAddr := <-s.newReadMessAddr
			newMess := newMessWithAddr.message
			newMessSenderAddr := newMessWithAddr.addr
			switch newMess.Type {
			case MsgConnect:
				logv.Printf("Network handler received new connection request\n")
				if _, found := s.clientAddrMap[newMessSenderAddr.String()]; found == false {
					logv.Printf("Network handler is adding new client at address %s\n", newMessSenderAddr.String())
					//construct new client and add it to clientMap
					newClientConn := new(clientConn)
					newClientConn.clientAddr = newMessSenderAddr
					newClientConn.connID = s.currConnID
					newClientConn.didReceiveFirstData = false
					newClientConn.windowRangeMin = 1
					newClientConn.windowRangeMax = newClientConn.windowRangeMin + s.params.WindowSize - 1
					newClientConn.windowWriteRangeMin = 1
					newClientConn.windowWriteRangeMax = newClientConn.windowWriteRangeMin + s.params.WindowSize - 1
					newClientConn.currSeqNum = 1
					newClientConn.nextBufferMessageNum = 1
					newClientConn.epochCount = 0
					newClientConn.messagesToWrite = list.New()
					newClientConn.receivedWriteMessages = make(map[int]Message)
					newClientConn.prevWriteMessages = make(map[int]bool)
					newClientConn.receivedMessages = make(map[int]Message)
					s.clientMap[s.currConnID] = newClientConn
					s.clientAddrMap[newMessSenderAddr.String()] = newClientConn
					sendAckS(s, 0, newClientConn)
					s.currConnID += 1
				}
			case MsgData:
				logv.Printf("Network handler received new data message\n")
				currClient := s.clientAddrMap[newMessSenderAddr.String()]
				logv.Printf("Data message at connID=%d seqNum=%d\n", currClient.connID, newMess.SeqNum)
				if currClient.didReceiveFirstData == false {
					currClient.didReceiveFirstData = true
					logv.Println("Server received first data")
				}
				//if it's in sliding window, add to map and buffer
				//otherwise adjust window
				if newMess.SeqNum > currClient.windowRangeMax {
					for i := currClient.windowRangeMin; i < currClient.windowRangeMin+(newMess.SeqNum-currClient.windowRangeMax); i++ {
						delete(currClient.receivedMessages, i)
					}
					currClient.windowRangeMin += newMess.SeqNum - currClient.windowRangeMax
					currClient.windowRangeMax = newMess.SeqNum
					currClient.receivedMessages[newMess.SeqNum] = newMess
					logv.Printf("Server updating readbuffer with connID=%d\n", currClient.connID)
					if newMess.SeqNum == currClient.nextBufferMessageNum {
						updateReadBufferS(s, currClient)
					}
					logv.Printf("server calling sendAckS\n")
					sendAckS(s, newMess.SeqNum, currClient)
				} else if newMess.SeqNum >= currClient.windowRangeMin {
					if _, found := currClient.receivedMessages[newMess.SeqNum]; found == false {
						currClient.receivedMessages[newMess.SeqNum] = newMess
						//check if we have a sequence of data in a row to add to buffer
						if newMess.SeqNum == currClient.nextBufferMessageNum {
							updateReadBufferS(s, currClient)
						}
						logv.Printf("server calling sendAckS\n")
						sendAckS(s, newMess.SeqNum, currClient)
					}
				}
				logv.Printf("New window for connID=%d: min=%d max=%d\n", currClient.connID, currClient.windowRangeMin, currClient.windowRangeMax)
			case MsgAck:
				currClient := s.clientAddrMap[newMessSenderAddr.String()]
				if newMess.SeqNum == currClient.windowWriteRangeMin {
					logv.Printf("Server received ack, adjusting window for pending writes\n")
					//find new window region
					minInd := currClient.windowWriteRangeMin + s.params.WindowSize
					currClient.prevWriteMessages[newMess.SeqNum] = true
					delete(currClient.receivedWriteMessages, newMess.SeqNum)
					for i := currClient.windowWriteRangeMin + 1; i <= currClient.windowWriteRangeMax; i++ {
						if _, found := currClient.prevWriteMessages[i]; found == false {
							minInd = i
							break
						}
					}
					logv.Printf("New minind = %d\n", minInd)
					oldMax := currClient.windowWriteRangeMax
					currClient.windowWriteRangeMin = minInd
					currClient.windowWriteRangeMax = currClient.windowWriteRangeMin + s.params.WindowSize - 1
					//if we slid the window, add messages from writeMessBuffer to sliding window if in range
					for i := 0; i < currClient.windowWriteRangeMax-oldMax; i++ {
						if currClient.messagesToWrite.Len() != 0 {
							currMess := currClient.messagesToWrite.Front().Value.(Message)
							if currMess.SeqNum >= currClient.windowWriteRangeMin && currMess.SeqNum <= currClient.windowWriteRangeMax {
								currClient.receivedWriteMessages[currMess.SeqNum] = currMess
								currClient.messagesToWrite.Remove(currClient.messagesToWrite.Front())
								writeMessS(s, currClient, currMess)
							}
						} else {
							break
						}
					}
				} else if newMess.SeqNum > currClient.windowWriteRangeMin && newMess.SeqNum <= currClient.windowWriteRangeMax {
					if _, found := currClient.prevWriteMessages[newMess.SeqNum]; found == false {
						currClient.prevWriteMessages[newMess.SeqNum] = true
						delete(currClient.receivedWriteMessages, newMess.SeqNum)
					}
				}
			}
			/*case closeConn := <- s.closeConn:
			for closeConn.receivedMessages*/
			//send acks
			/*for _, message := range c.receivedMessages {
				sendAck(c, message.SeqNum)
			}*/
			//send non ack'd data messages
			/*for _, message := range c.receivedWriteMessages {
				writeMess(c, message)
			}*/
			/*case unmarshMess := <- c.receivedMess:
			switch unmarshMess.MsgType {
			case MsgData:
			case MsgAck:
			}*/
			/*case <-c.shutdown:
			logv.Printf("Master go routine shutting down\n")
			return nil*/
		}
	}
	return nil
}

//func networkHandlerS(s *server) {
//	go readInS(s)
//	for {
//		select {
//		case newMessWithAddr := <-s.readInMessWithAddr:
//			//newMessSenderAddr := <-s.readInMessSenderAddr
//			//s.newReadMess <- newMess
//			//s.newReadMessAddr <- newMessSenderAddr
//			s.newReadMessWithAddr <- newMessWithAddr
//			/*switch newMess.Type {
//			case MsgConnect:
//				logv.Printf("Network handler received new connection request\n")
//				if _, found := s.clientAddrMap[newMessSenderAddr.String()]; found == false {
//					logv.Printf("Network handler is adding new client at address %s\n",newMessSenderAddr.String())
//					//construct new client and add it to clientMap
//					newClientConn := new(clientConn)
//					newClientConn.clientAddr = newMessSenderAddr
//					newClientConn.connID = s.currConnID
//					newClientConn.didReceiveFirstData = false
//					newClientConn.windowRangeMin = 1
//					newClientConn.windowRangeMax = newClientConn.windowRangeMin + s.params.WindowSize - 1
//					newClientConn.windowWriteRangeMin = 1
//					newClientConn.windowWriteRangeMax = newClientConn.windowWriteRangeMin + s.params.WindowSize - 1
//					newClientConn.currSeqNum = 1
//					newClientConn.nextBufferMessageNum = 1
//					newClientConn.messagesToWrite = list.New()
//					newClientConn.receivedWriteMessages = make(map[int]Message)
//					newClientConn.prevWriteMessages = make(map[int]bool)
//					newClientConn.receivedMessages = make(map[int]Message)
//					s.clientMap[s.currConnID] = newClientConn
//					s.clientAddrMap[newMessSenderAddr.String()] = newClientConn
//					sendAckS(s, 0, newClientConn)
//					s.currConnID += 1
//				}
//			case MsgData:
//				logv.Printf("Network handler received new data message: %+v\n", newMess)
//				currClient := s.clientAddrMap[newMessSenderAddr.String()]
//				if currClient.didReceiveFirstData == false {
//					currClient.didReceiveFirstData = true
//					logv.Println("Client received first data")
//				}
//				//if it's in sliding window, add to map and buffer
//				//otherwise adjust window
//				if newMess.SeqNum > currClient.windowRangeMax {
//					for i := currClient.windowRangeMin; i < currClient.windowRangeMin+(newMess.SeqNum-currClient.windowRangeMax); i++ {
//						delete(currClient.receivedMessages, i)
//					}
//					currClient.windowRangeMin += newMess.SeqNum - currClient.windowRangeMax
//					currClient.windowRangeMax = newMess.SeqNum
//					currClient.receivedMessages[newMess.SeqNum] = newMess
//					updateReadBufferS(s, currClient)
//					sendAckS(s, newMess.SeqNum, currClient)
//				} else if newMess.SeqNum >= currClient.windowRangeMin {
//					if _, found := currClient.receivedMessages[newMess.SeqNum]; found == false {
//						currClient.receivedMessages[newMess.SeqNum] = newMess
//						//check if we have a sequence of data in a row to add to buffer
//						updateReadBufferS(s, currClient)
//						sendAckS(s, newMess.SeqNum, currClient)
//					}
//				}
//			case MsgAck:
//				currClient := s.clientAddrMap[newMessSenderAddr.String()]
//				if newMess.SeqNum == currClient.windowWriteRangeMin {
//					//find new window region
//					minInd := currClient.windowWriteRangeMin + s.params.WindowSize
//					currClient.prevWriteMessages[newMess.SeqNum] = true
//					delete(currClient.receivedWriteMessages, newMess.SeqNum)
//					for i := currClient.windowWriteRangeMin + 1; i <= currClient.windowWriteRangeMax; i++ {
//						if _, found := currClient.prevWriteMessages[i]; found == false {
//							minInd = i
//							break
//						}
//					}
//					oldMax := currClient.windowRangeMax
//					currClient.windowWriteRangeMin = minInd
//					currClient.windowWriteRangeMax = currClient.windowWriteRangeMin + s.params.WindowSize - 1
//					//if we slid the window, add messages from writeMessBuffer to sliding window if in range
//					for i := 0; i < currClient.windowWriteRangeMax-oldMax; i++ {
//						if currClient.messagesToWrite.Len() != 0 {
//							currMess := currClient.messagesToWrite.Front().Value.(Message)
//							if currMess.SeqNum >= currClient.windowWriteRangeMin && currMess.SeqNum <= currClient.windowWriteRangeMax {
//								currClient.receivedWriteMessages[currMess.SeqNum] = currMess
//								currClient.messagesToWrite.Remove(currClient.messagesToWrite.Front())
//							}
//						} else {
//							break
//						}
//					}
//				} else if newMess.SeqNum > currClient.windowWriteRangeMin && newMess.SeqNum <= currClient.windowWriteRangeMax {
//					if _, found := currClient.prevWriteMessages[newMess.SeqNum]; found == false {
//						currClient.prevWriteMessages[newMess.SeqNum] = true
//						delete(currClient.receivedWriteMessages, newMess.SeqNum)
//					}
//				}
//			}
//			s.doneWithNewReadMess <- struct{}{}*/
//		case <-s.shutdown:
//			logv.Println("Network handler shutting down\n")
//			return
//		}
//	}
//	return
//}

//func epochHandlerS(s *server, epochLimit int, epochDuration int) {
//	epochCount := 0
//	epochFired := time.Tick(time.Duration(epochDuration) * time.Millisecond)
//	for {
//		select {
//		case <-epochFired:
//			logv.Printf("Server epoch handler fired, count=%d\n", epochCount)
//			epochCount += 1
//			s.epochDidOccur <- epochCount
//		case <-s.resetEpochCount:
//			epochCount = 0
//			/*case <-c.didRequestEpochCount:
//			c.epochCount <- epochCount*/
//		case <-s.shutdown:
//			return
//		}
//	}
//}

func readInS(s *server) {
	for {
		select {
		case <-s.shutdown:
			return
		default:
			var receivedMarshMess [1500]byte
			nRec, senderAddr, readError := s.conn.ReadFromUDP(receivedMarshMess[:])
			if readError != nil {
				//return
			}
			var unmarshMess Message
			unmarshError := json.Unmarshal(receivedMarshMess[0:nRec], &unmarshMess)
			if unmarshError != nil {
				//return
			}
			if unmarshMess.Type != MsgConnect {
				s.resetEpochCount <- unmarshMess.ConnID
			}
			logv.Printf("readIn read new message\n")
			readInMessWithAddr := new(messageWithAddr)
			readInMessWithAddr.message = unmarshMess
			readInMessWithAddr.addr = senderAddr
			//s.readInMess <- unmarshMess
			//s.readInMessSenderAddr <- senderAddr
			s.readInMessWithAddr <- *readInMessWithAddr
		}
	}
}

func sendAckS(s *server, seqNum int, client *clientConn) error {
	logv.Printf("Server sending ack connID=%d seqNum=%d\n", client.connID, seqNum)
	ackMess := NewAck(client.connID, seqNum)
	marshMess, marshError := json.Marshal(ackMess)
	if marshError != nil {
		return marshError
	}
	_, writeError := s.conn.WriteToUDP(marshMess, client.clientAddr)
	if writeError != nil {
		return writeError
	}
	return nil
}

func updateReadBufferS(s *server, c *clientConn) {
	var i int
	for i = c.nextBufferMessageNum; i <= c.windowRangeMax; i++ {
		if val, found := c.receivedMessages[i]; found == true {
			newMessWithID := new(messageWithID)
			newMessWithID.connID = c.connID
			newMessWithID.message = val
			s.messagesToReadIn <- *newMessWithID
		} else {
			break
		}
	}
	c.nextBufferMessageNum = i
}

func writeMessS(s *server, client *clientConn, mess Message) error {
	marshMess, marshError := json.Marshal(mess)
	if marshError != nil {
		return marshError
	}
	_, writeError := s.conn.WriteToUDP(marshMess, client.clientAddr)
	if writeError != nil {
		return writeError
	}
	return nil
}

func handleBufferS(in <-chan messageWithID, out chan<- messageWithID, s *server) {
	defer close(out)

	// This list will store all values received from 'in'.
	// All values should eventually be sent back through 'out',
	// even if the 'in' channel is suddenly closed.
	buffer := list.New()

	for {
		select {
		case <-s.shutdown:
			return
		default:
			// Make sure that the list always has values before
			// we select over the two channels.
			if buffer.Len() == 0 {
				select {
				case v, ok := <-in:
					if !ok {
						// 'in' has been closed. Flush all values
						// in the buffer and return.
						flushS(buffer, out, s)
						return
					}
					buffer.PushBack(v)
				}
			}

			select {
			case v, ok := <-in:
				if !ok {
					// 'in' has been closed. Flush all values
					// in the buffer and return.
					flushS(buffer, out, s)
					return
				}
				buffer.PushBack(v)
			case out <- (buffer.Front().Value).(messageWithID):
				buffer.Remove(buffer.Front())
			}
		}
	}
}

// Blocks until all values in the buffer have been sent through
// the 'out' channel.
func flushS(buffer *list.List, out chan<- messageWithID, s *server) {
	for e := buffer.Front(); e != nil; e = e.Next() {
		out <- (e.Value).(messageWithID)
	}
	s.doneFlushingReadOut <- struct{}{}
}
func handleBufferS2(in <-chan int, out chan<- int, s *server) {
	defer close(out)

	// This list will store all values received from 'in'.
	// All values should eventually be sent back through 'out',
	// even if the 'in' channel is suddenly closed.
	buffer := list.New()

	for {
		select {
		case <-s.shutdown:
			return
		default:
			// Make sure that the list always has values before
			// we select over the two channels.
			if buffer.Len() == 0 {
				select {
				case v, ok := <-in:
					if !ok {
						// 'in' has been closed. Flush all values
						// in the buffer and return.
						flushS2(buffer, out, s)
						return
					}
					buffer.PushBack(v)
				}
			}

			select {
			case v, ok := <-in:
				if !ok {
					// 'in' has been closed. Flush all values
					// in the buffer and return.
					flushS2(buffer, out, s)
					return
				}
				buffer.PushBack(v)
			case out <- (buffer.Front().Value).(int):
				buffer.Remove(buffer.Front())
			}
		}
	}
}

// Blocks until all values in the buffer have been sent through
// the 'out' channel.
func flushS2(buffer *list.List, out chan<- int, s *server) {
	for e := buffer.Front(); e != nil; e = e.Next() {
		out <- (e.Value).(int)
	}
	s.doneFlushingReadOut <- struct{}{}
}
