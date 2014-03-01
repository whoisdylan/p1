// Contains the implementation of a LSP client.

package lsp

import "errors"
import "time"
import "github.com/cmu440/lspnet"
import "encoding/json"
import "os"
import "io/ioutil"
import "log"
import "container/list"

//logs
var loge = log.New(os.Stderr, "ERROR ", log.Lmicroseconds|log.Lshortfile)

var logv = log.New(ioutil.Discard, "VERBOSE ", log.Lmicroseconds|log.Lshortfile)

//var logv = log.New(os.Stdout, "V ", log.Lmicroseconds|log.Lshortfile)

type chanSignal chan struct{}

type status struct {
	didReceiveFirstData bool
}

type client struct {
	connID int
	params *Params

	//sliding window things
	//window for received messages
	windowRangeMin int
	windowRangeMax int
	//window for sending messages
	windowWriteRangeMin int
	windowWriteRangeMax int

	//epochHandler things
	//epochCount           chan int
	//didRequestEpochCount chanSignal
	resetEpochCount chanSignal
	epochDidOccur   chan int

	//networkHandler things
	readServerResponse chanSignal
	receivedMess       chan Message

	//readIn things
	readInMess chan Message

	//message things
	receivedMessages  map[int]Message
	messagesToReadIn  chan Message
	messagesToReadOut chan Message
	//messagesToWriteIn  chan Message
	//messagesToWriteOut chan Message
	messagesToWrite     *list.List
	doneFlushingReadOut chanSignal

	newWriteMess chan Message
	newReadMess  chan Message
	//doneWithNewWriteMess chanSignal
	//doneWithNewReadMess chanSignal

	//write things
	currSeqNum            int //seqNum counter for Write
	receivedWriteMessages map[int]Message
	prevWriteMessages     map[int]bool //for checking if write messages were previously ackd

	nextBufferMessageNum int

	serverConn *lspnet.UDPConn
	shutdown   chanSignal

	closeCalled bool
	doneWriting chanSignal

	//status struct
	clientStatus *status
}

// NewClient creates, initiates, and returns a new client. This function
// should return after a connection with the server has been established
// (i.e., the client has received an Ack message from the server in response
// to its connection request), and should return a non-nil error if a
// connection could not be made (i.e., if after K epochs, the client still
// hasn't received an Ack message from the server in response to its K
// connection requests).
//
// hostport is a colon-separated string identifying the server's host address
// and port number (i.e., "localhost:9999").
func NewClient(hostport string, params *Params) (Client, error) {
	//start goroutines for master, networkHandler, epochHandler
	//init client struct
	c := new(client)
	c.clientStatus = new(status)
	c.clientStatus.didReceiveFirstData = false
	c.params = params
	//when seqNum n gets ack'd, set windowRangeMin to n+1 (if you are the min) and update max
	//need to check what to set the min to, need to have list of acks?
	c.windowRangeMin = 1
	c.windowRangeMax = c.windowRangeMin + c.params.WindowSize - 1
	c.windowWriteRangeMin = 1
	c.windowWriteRangeMax = c.windowRangeMin + c.params.WindowSize - 1
	c.epochDidOccur = make(chan int)
	c.shutdown = make(chanSignal)
	//c.epochCount = make(chan int)
	//c.didRequestEpochCount = make(chanSignal)
	c.resetEpochCount = make(chanSignal)
	c.readServerResponse = make(chanSignal)
	c.receivedMess = make(chan Message)
	c.readInMess = make(chan Message)
	c.messagesToReadIn = make(chan Message)
	c.messagesToReadOut = make(chan Message)
	//c.messagesToWriteIn = make(chan Message)
	//c.messagesToWriteOut = make(chan Message)
	c.messagesToWrite = list.New()
	c.doneFlushingReadOut = make(chanSignal)
	c.receivedMessages = make(map[int]Message)
	c.receivedWriteMessages = make(map[int]Message)
	c.prevWriteMessages = make(map[int]bool)
	c.nextBufferMessageNum = 1
	c.currSeqNum = 1
	//c.doneWithNewWriteMess = make(chanSignal)
	c.newWriteMess = make(chan Message)
	//c.doneWithNewReadMess = make(chanSignal)
	c.newReadMess = make(chan Message)
	c.closeCalled = false
	c.doneWriting = make(chanSignal)

	//create and marshal the connect message
	connMessage := NewConnect()
	marshMess, marshError := json.Marshal(connMessage)
	if marshError != nil {
		return nil, marshError
	}

	connAddr, resolveError := lspnet.ResolveUDPAddr("udp", hostport)
	if resolveError != nil {
		return nil, resolveError
	}

	//go epochHandler(c, params.EpochLimit, params.EpochMillis)
	epochCount := 0
	epochFired := time.Tick(time.Duration(c.params.EpochMillis) * time.Millisecond)

	//try to connect to server
	var connError error
	c.serverConn, connError = lspnet.DialUDP("udp", nil, connAddr)
	if connError != nil {
		return nil, connError
	}
	/*for connError != nil {
			select {
			case epochCount := <-c.epochDidOccur:
				//c.didRequestEpochCount <- struct{}{}
				//epochCount = <-c.epochCount
				if epochCount >= params.EpochLimit {
	//				logv.Println("client epoch limit reached while waiting for connect ack")
					close(c.shutdown)
					return nil, connError
				}
				c.serverConn, connError = lspnet.DialUDP("udp", nil, connAddr)
			}
		}*/
	//c.resetEpochCount <- struct{}{}
	//go networkHandler(c)
	go readIn(c)

	_, writeError := c.serverConn.Write(marshMess)
	if writeError != nil {
		return nil, writeError
	}
	logv.Println("Client wrote connect message to server")
	//c.readServerResponse <- struct{}{}
	//var receivedMess Message
	//nRec, readError := c.serverConn.Read(receivedMarshMess)
	for {
		select {
		case <-epochFired:
			epochCount += 1
			//c.didRequestEpochCount <- struct{}{}
			//epochCount = <-c.epochCount
			if epochCount >= params.EpochLimit {
				logv.Println("client epoch limit reached while waiting for write ack")
				close(c.shutdown)
				return nil, errors.New("Failed to connect to server, epoch limit exceeded")
			}
			_, writeError = c.serverConn.Write(marshMess)
		case <-c.resetEpochCount:
			epochCount = 0
		case receivedMess := <-c.readInMess:
			if receivedMess.Type == MsgAck {
				logv.Println("Client received connect ack from server")
				c.connID = receivedMess.ConnID
				go handleBuffer(c.messagesToReadIn, c.messagesToReadOut, c)
				//go handleBuffer(c.messagesToWriteIn, c.messagesToWriteOut, c)
				go master(c)
				return c, nil
			}
		}
	}
	return nil, nil
}

func (c *client) ConnID() int {
	return c.connID
}

func (c *client) Read() ([]byte, error) {
	select {
	case <-c.shutdown:
		return nil, errors.New("Cannot Read, connection closed")
	case newMess := <-c.messagesToReadOut:
		return newMess.Payload, nil
	}
	return nil, nil
}

func (c *client) Write(payload []byte) error {
	select {
	case <-c.shutdown:
		return errors.New("Could not write, connection closed")
	default:
		logv.Printf("Write is creating new data message\n")
		c.currSeqNum += 1
		newWriteMess := *NewData(c.connID, c.currSeqNum-1, payload)
		c.newWriteMess <- newWriteMess
		/*if newWriteMess.SeqNum >= c.windowWriteRangeMin && newWriteMess.SeqNum <= c.windowWriteRangeMax {
			c.receivedWriteMessages[newWriteMess.SeqNum] = newWriteMess
			writeMess(c, newWriteMess)
		} else {
			c.messagesToWrite.PushBack(newWriteMess)
			//c.messagesToWriteIn <- newWriteMess
		}
		c.currSeqNum += 1
		c.doneWithNewWriteMess <- struct{}{}*/
	}
	return nil
}

func writeMess(c *client, mess Message) error {
	marshMess, marshError := json.Marshal(mess)
	if marshError != nil {
		return marshError
	}
	_, writeError := c.serverConn.Write(marshMess)
	if writeError != nil {
		return writeError
	}
	return nil
}

func (c *client) Close() error {
	logv.Printf("Client Close called\n")
	c.closeCalled = true
	for {
		select {
		case <-c.shutdown:
			return nil
		case <-c.doneWriting:
			c.serverConn.Close()
			return nil
			/*default:
			//block until all messages that are ready to write have been ackd
			for c.messagesToWrite.Len() != 0 || len(c.receivedWriteMessages) != 0 {

			}
			c.serverConn.Close()
			//close(c.shutdown)
			return nil*/
		}
	}
}

func master(c *client) error {
	epochCount := 0
	epochFired := time.Tick(time.Duration(c.params.EpochMillis) * time.Millisecond)
	for {
		select {
		//case epochCount := <-c.epochDidOccur:
		case <-epochFired:
			epochCount += 1
			logv.Printf("Client epoch fired and handled by master")
			//c.didRequestEpochCount <- struct{}{}
			//epochCount := <-c.epochCount
			if epochCount >= c.params.EpochLimit {
				//if epoch limit exceeded, let client Read all extra data
				updateReadBuffer(c)
				close(c.messagesToReadIn)
				<-c.doneFlushingReadOut
				close(c.shutdown)
				return errors.New("Client epoch limit reached in master go routine")
			}
			if c.clientStatus.didReceiveFirstData == false {
				logv.Printf("Client hasn't received first data, sending ack\n")
				//send an ack with seqNum = 0
				sendAck(c, 0)
			}
			//send acks
			logv.Printf("Client sending acks for received messages, size=%d\n", len(c.receivedMessages))
			for _, message := range c.receivedMessages {
				logv.Printf("acking SeqNum=%d\n", message.SeqNum)
				sendAck(c, message.SeqNum)
			}
			//send non ack'd data messages
			logv.Printf("Client resending non ack'd messages, size=%d\n", len(c.receivedWriteMessages))
			for _, message := range c.receivedWriteMessages {
				logv.Printf("Resending SeqNum=%d\n", message.SeqNum)
				writeMess(c, message)
			}
		case <-c.resetEpochCount:
			epochCount = 0
		case newMess := <-c.readInMess:
			switch newMess.Type {
			case MsgData:
				logv.Printf("Master handler received new data message\n")
				if c.clientStatus.didReceiveFirstData == false {
					c.clientStatus.didReceiveFirstData = true
					logv.Println("Client received first data")
				}
				//if it's in sliding window, add to map and buffer
				//otherwise adjust window
				if newMess.SeqNum > c.windowRangeMax {
					logv.Println("Client is adjusting sliding window A")
					for i := c.windowRangeMin; i < c.windowRangeMin+(newMess.SeqNum-c.windowRangeMax); i++ {
						delete(c.receivedMessages, i)
					}
					c.windowRangeMin += newMess.SeqNum - c.windowRangeMax
					c.windowRangeMax = newMess.SeqNum
					c.receivedMessages[newMess.SeqNum] = newMess
					updateReadBuffer(c)
					sendAck(c, newMess.SeqNum)
				} else if newMess.SeqNum >= c.windowRangeMin {
					logv.Printf("Client received data message within window\n")
					if _, found := c.receivedMessages[newMess.SeqNum]; found == false {
						c.receivedMessages[newMess.SeqNum] = newMess
						//check if we have a sequence of data in a row to add to buffer
						updateReadBuffer(c)
						sendAck(c, newMess.SeqNum)
					}
				}
			case MsgAck:
				if newMess.SeqNum == c.windowWriteRangeMin {
					//find new window region
					minInd := c.windowWriteRangeMin + c.params.WindowSize
					c.prevWriteMessages[newMess.SeqNum] = true
					delete(c.receivedWriteMessages, newMess.SeqNum)
					for i := c.windowWriteRangeMin + 1; i <= c.windowWriteRangeMax; i++ {
						if _, found := c.prevWriteMessages[i]; found == false {
							minInd = i
							break
						}
					}
					oldMax := c.windowRangeMax
					c.windowWriteRangeMin = minInd
					c.windowWriteRangeMax = c.windowWriteRangeMin + c.params.WindowSize - 1
					//if we slid the window, add messages from writeMessBuffer to sliding window if in range
					for i := 0; i < c.windowWriteRangeMax-oldMax; i++ {
						if c.messagesToWrite.Len() != 0 {
							currMess := c.messagesToWrite.Front().Value.(Message)
							if currMess.SeqNum >= c.windowWriteRangeMin && currMess.SeqNum <= c.windowWriteRangeMax {
								c.receivedWriteMessages[currMess.SeqNum] = currMess
								c.messagesToWrite.Remove(c.messagesToWrite.Front())
								writeMess(c, currMess)
							}
						} else {
							break
						}
					}
				} else if newMess.SeqNum > c.windowWriteRangeMin && newMess.SeqNum <= c.windowWriteRangeMax {
					if _, found := c.prevWriteMessages[newMess.SeqNum]; found == false {
						c.prevWriteMessages[newMess.SeqNum] = true
						delete(c.receivedWriteMessages, newMess.SeqNum)
					}
				}
				if c.closeCalled && c.messagesToWrite.Len() == 0 {
					c.doneWriting <- struct{}{}
				}
			}
		case newWriteMess := <-c.newWriteMess:
			if newWriteMess.SeqNum >= c.windowWriteRangeMin && newWriteMess.SeqNum <= c.windowWriteRangeMax {
				logv.Printf("Client adding new write message to window seqNum=%d\n", newWriteMess.SeqNum)
				c.receivedWriteMessages[newWriteMess.SeqNum] = newWriteMess
				writeMess(c, newWriteMess)
			} else {
				logv.Printf("Client appending write message to buffer seqNum=%d\n", newWriteMess.SeqNum)
				c.messagesToWrite.PushBack(newWriteMess)
				//c.messagesToWriteIn <- newWriteMess
			}
			/*case unmarshMess := <- c.receivedMess:
			switch unmarshMess.MsgType {
			case MsgData:
			case MsgAck:
			}*/
			/*case <-c.shutdown:
			//			logv.Printf("Master go routine shutting down\n")
						return nil*/
		}
	}
	return nil
}

func epochHandler(c *client, epochLimit int, epochDuration int) {
	epochCount := 0
	epochFired := time.Tick(time.Duration(epochDuration) * time.Millisecond)
	for {
		select {
		case <-epochFired:
			epochCount += 1
			c.epochDidOccur <- epochCount
		case <-c.resetEpochCount:
			epochCount = 0
		/*case <-c.didRequestEpochCount:
		c.epochCount <- epochCount*/
		case <-c.shutdown:
			return
		}
	}
}

//func networkHandler(c *client) error {
//	go readIn(c)
//	for {
//		select {
//		/*case <-c.readServerResponse:
//		var receivedMarshMess [1500]byte
//		nRec, readError := c.serverConn.Read(receivedMarshMess[:])
//		if readError != nil {
//			//return
//		}
//		c.resetEpochCount <- struct{}{}
//		var unmarshMess Message
//		unmarshError := json.Unmarshal(receivedMarshMess[0:nRec], &unmarshMess)
//		if unmarshError != nil {
//			//return
//		}
//		c.receivedMess <- unmarshMess*/
//		case newMess := <-c.readInMess:
//			c.newReadMess <- newMess
//			/*switch newMess.Type {
//			case MsgData:
//				logv.Printf("Network handler received new data message: %+v\n", newMess)
//				if c.clientStatus.didReceiveFirstData == false {
//					c.clientStatus.didReceiveFirstData = true
//					logv.Println("Client received first data")
//				}
//				//if it's in sliding window, add to map and buffer
//				//otherwise adjust window
//				if newMess.SeqNum > c.windowRangeMax {
//					for i := c.windowRangeMin; i < c.windowRangeMin+(newMess.SeqNum-c.windowRangeMax); i++ {
//						delete(c.receivedMessages, i)
//					}
//					c.windowRangeMin += newMess.SeqNum - c.windowRangeMax
//					c.windowRangeMax = newMess.SeqNum
//					c.receivedMessages[newMess.SeqNum] = newMess
//					updateReadBuffer(c)
//					sendAck(c, newMess.SeqNum)
//				} else if newMess.SeqNum >= c.windowRangeMin {
//					if _, found := c.receivedMessages[newMess.SeqNum]; found == false {
//						c.receivedMessages[newMess.SeqNum] = newMess
//						//check if we have a sequence of data in a row to add to buffer
//						updateReadBuffer(c)
//						sendAck(c, newMess.SeqNum)
//					}
//				}
//			case MsgAck:
//				if newMess.SeqNum == c.windowWriteRangeMin {
//					//find new window region
//					minInd := c.windowWriteRangeMin + c.params.WindowSize
//					c.prevWriteMessages[newMess.SeqNum] = true
//					delete(c.receivedWriteMessages, newMess.SeqNum)
//					for i := c.windowWriteRangeMin + 1; i <= c.windowWriteRangeMax; i++ {
//						if _, found := c.prevWriteMessages[i]; found == false {
//							minInd = i
//							break
//						}
//					}
//					oldMax := c.windowRangeMax
//					c.windowWriteRangeMin = minInd
//					c.windowWriteRangeMax = c.windowWriteRangeMin + c.params.WindowSize - 1
//					//if we slid the window, add messages from writeMessBuffer to sliding window if in range
//					for i := 0; i < c.windowWriteRangeMax-oldMax; i++ {
//						if c.messagesToWrite.Len() != 0 {
//							currMess := c.messagesToWrite.Front().Value.(Message)
//							if currMess.SeqNum >= c.windowWriteRangeMin && currMess.SeqNum <= c.windowWriteRangeMax {
//								c.receivedWriteMessages[currMess.SeqNum] = currMess
//								c.messagesToWrite.Remove(c.messagesToWrite.Front())
//							}
//						} else {
//							break
//						}
//					}
//				} else if newMess.SeqNum > c.windowWriteRangeMin && newMess.SeqNum <= c.windowWriteRangeMax {
//					if _, found := c.prevWriteMessages[newMess.SeqNum]; found == false {
//						c.prevWriteMessages[newMess.SeqNum] = true
//						delete(c.receivedWriteMessages, newMess.SeqNum)
//					}
//				}
//			}
//			c.doneWithNewReadMess <- struct{}{}*/
//		/*case newWriteMess := <-c.messagesToWriteOut:
//		if newWriteMess.SeqNum < c.windowWriteRangeMax && newWriteMess.SeqNum >= c.windowWriteRangeMin {
//
//		}*/
//		case <-c.shutdown:
//			return nil
//		}
//	}
//	return nil
//}

func updateReadBuffer(c *client) {
	var i int
	for i = c.nextBufferMessageNum; i <= c.windowRangeMax; i++ {
		if val, found := c.receivedMessages[i]; found == true {
			c.messagesToReadIn <- val
		} else {
			break
		}
	}
	c.nextBufferMessageNum = i
}

func sendAck(c *client, seqNum int) error {
	logv.Printf("Client sending ack to SeqNum=%d\n", seqNum)
	ackMess := NewAck(c.connID, seqNum)
	marshMess, marshError := json.Marshal(ackMess)
	if marshError != nil {
		return marshError
	}
	_, writeError := c.serverConn.Write(marshMess)
	if writeError != nil {
		return writeError
	}
	return nil
}

func readIn(c *client) error {
	for {
		select {
		case <-c.shutdown:
			return nil
		default:
			var receivedMarshMess [1500]byte
			nRec, readError := c.serverConn.Read(receivedMarshMess[:])
			if readError != nil {
				//return
			}
			c.resetEpochCount <- struct{}{}
			var unmarshMess Message
			unmarshError := json.Unmarshal(receivedMarshMess[0:nRec], &unmarshMess)
			if unmarshError != nil {
				//return
			}
			c.readInMess <- unmarshMess
		}
	}
	return nil
}

func handleBuffer(in <-chan Message, out chan<- Message, c *client) {
	defer close(out)

	// This list will store all values received from 'in'.
	// All values should eventually be sent back through 'out',
	// even if the 'in' channel is suddenly closed.
	buffer := list.New()

	for {
		select {
		case <-c.shutdown:
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
						flush(buffer, out, c)
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
					flush(buffer, out, c)
					return
				}
				buffer.PushBack(v)
			case out <- (buffer.Front().Value).(Message):
				buffer.Remove(buffer.Front())
			}
		}
	}
}

// Blocks until all values in the buffer have been sent through
// the 'out' channel.
func flush(buffer *list.List, out chan<- Message, c *client) {
	for e := buffer.Front(); e != nil; e = e.Next() {
		out <- (e.Value).(Message)
	}
	c.doneFlushingReadOut <- struct{}{}
}
