package main

import (
	"fmt"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/truechain/truechain-engineering-code/rpc"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//Count send complete
var Count int64

//Transaction from to account id
var from, to, frequency = 0, 0, 1

//Two transmission intervals
var interval = time.Millisecond * 0

//get all account
var account []string

//get all account
var noBalance []int

// The message state
var msg = make(chan bool)

// Restart the number
var num int

// SLEEPTIME The interval between reconnections
const SLEEPTIME = 120

// SLEEPTX The interval between send son address
const SLEEPTX = 5

var gensisAccount1 = "0x7c357530174275dd30e46319b89f71186256e4f7"
var gensisAccount2 = "0x4cf807958b9f6d9fd9331397d7a89a079ef43288"

// get par
func main() {
	if len(os.Args) < 4 {
		fmt.Printf("invalid args : %s [count] [frequency] [interval] [from] [to] [\"port\"]\n", os.Args[0])
		return
	}

	count, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Println("count err")
		return
	}

	frequency, err = strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Println("frequency err")
		return
	}

	intervalCount, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Println("interval err")
		return
	}

	interval = time.Millisecond * time.Duration(intervalCount)

	from, err = strconv.Atoi(os.Args[4])
	if err != nil {
		fmt.Println("from err default 0")
	}

	if len(os.Args) > 5 {
		to, err = strconv.Atoi(os.Args[5])
	} else {
		fmt.Println("to 0：Local address 1: Generate address")
	}

	ip := "127.0.0.1:"
	if len(os.Args) == 7 {
		ip = ip + os.Args[6]
	} else {
		ip = ip + "8888"
	}

	go send(count, ip)

	for {
		if !<-msg {
			fmt.Println("======================send Transaction restart=========================")
			num++
			time.Sleep(time.Second * SLEEPTIME)
			go send(count, ip)
		} else {
			fmt.Println("=======================send Transaction end=========================")
			break
		}
	}
	fmt.Println("send Transaction num is:", num)
}

//send transaction init
func send(count int, ip string) {
	//dial etrue
	client, err := rpc.Dial("http://" + ip)

	defer client.Close()

	if err != nil {
		fmt.Println("Dail:", ip, err.Error())
		msg <- false
		return
	}

	err = client.Call(&account, "etrue_accounts")
	if err != nil {
		fmt.Println("etrue_accounts Error", err.Error())
		msg <- false
		return
	}
	if len(account) == 0 {
		fmt.Println("no account")
		return
	}
	fmt.Println("already have accounts is in local:", len(account))

	for i := len(account); i < count; i++ {
		//new account
		var address string
		err = client.Call(&address, "personal_newAccount", "admin")
		if err != nil {
			fmt.Println("personal_newAccount Error:", err.Error())
			msg <- false
			return
		}
		account = append(account, address)
		fmt.Println("personal_newAccount ", i, " accounts ", " Ok ", len(account), "address", address)
	}
	fmt.Println("personal_newAccount success ", len(account), "main address ", account[from])

	// get balance
	var result string
	err = client.Call(&result, "etrue_getBalance", account[from], "latest")
	if err != nil {
		fmt.Println("etrue_getBalance Error:", err)
		msg <- false
		return
	}

	balance := getBalanceValue(result)

	//main unlock account
	_, err = unlockAccount(client, account[from], "admin", 9000000, "main")
	if err != nil {
		fmt.Println("personal_unlockAccount Error:", err.Error())
		msg <- false
		return
	}

	//send main to son address
	fmt.Println("send balance to ", count, "  new account ", sendBalanceNewAccount(client, count, balance))

	//son address unlock account
	fmt.Println("unlock ", count, " son account ", unlockCountNewAccount(client, count))

	// send
	fmt.Println("start sendTransactions from ", count, " account to other new account")
	waitMain := &sync.WaitGroup{}
	for {
		waitMain.Add(1)
		go sendTransactions(client, account, count, waitMain)
		frequency--
		if frequency <= 0 {
			break
		}
		time.Sleep(interval)
	}
	waitMain.Wait()
	msg <- true
}

//send count transaction
func sendTransactions(client *rpc.Client, account []string, count int, wait *sync.WaitGroup) {
	defer wait.Done()
	waitGroup := &sync.WaitGroup{}
	Time := time.Now()

	for i := 0; i < count; i++ {
		var result string
		// get balance
		err := client.Call(&result, "etrue_getBalance", account[i], "latest")
		if err != nil {
			fmt.Println("etrue_getBalance Error:", err)
			msg <- false
			return
		}
		balance := getBalanceValue(result)
		if balance.Cmp(big.NewInt(int64(100000))) < 0 {
			fmt.Println(" Lack of balance  ", balance, " i ", i)
			continue
		}

		waitGroup.Add(1)
		go sendTransaction(client, account[i], i, "", "0x2100", waitGroup)
	}
	waitGroup.Wait()
	fmt.Println(" Complete ", Count, " time ", Time, " count ", count)
}

//send one transaction
func sendTransaction(client *rpc.Client, from string, index int, son string, value string, wait *sync.WaitGroup) {
	defer wait.Done()

	var address string

	if son == "" {
		address = genAddress()
		if to == 1 {
			if account[to] != "" {
				address = account[to]
			}
		}
	} else {
		address = son
	}

	result, err := sendRawTransaction(client, from, address, value)

	if err != nil {
		fmt.Println("sendRawTransaction", "result ", result, "index", index, " error", err, " address ", address)
	}

	if result != "" {
		Count++
	}
}

func sendRawTransaction(client *rpc.Client, from string, to string, value string) (string, error) {

	mapData := make(map[string]interface{})

	mapData["from"] = from
	mapData["to"] = to
	mapData["value"] = value

	var result string
	err := client.Call(&result, "etrue_sendTransaction", mapData)
	return result, err
}

func unlockAccount(client *rpc.Client, account string, password string, time int, name string) (bool, error) {
	var reBool bool
	err := client.Call(&reBool, "personal_unlockAccount", account, password, time)
	fmt.Println(name, " personal_unlockAccount Ok", reBool)
	return reBool, err
}

// Genesis address
func genAddress() string {
	priKey, _ := crypto.GenerateKey()
	address := crypto.PubkeyToAddress(priKey.PublicKey)
	return address.Hex()
}

func getBalanceValue(hex string) *big.Int {
	if strings.HasPrefix(hex, "0x") {
		hex = strings.TrimPrefix(hex, "0x")
	}
	value, _ := new(big.Int).SetString(hex, 16)
	balance := new(big.Int).Set(value)
	fmt.Println("etrue_getBalance Ok:", " true ", balance.Div(balance, big.NewInt(1000000000000000000)), " value ", value, " hex ", hex)
	return value
}

func sendBalanceNewAccount(client *rpc.Client, count int, main *big.Int) bool {
	var result string
	find := false
	getBalance := true
	average := main.Div(main, big.NewInt(int64(len(account)*2)))
	value := "0x" + fmt.Sprintf("%x", average)
	averageTrue := new(big.Int).Set(average)
	fmt.Println("sendBalanceNewAccount ", " true ", averageTrue.Div(averageTrue, big.NewInt(1000000000000000000)), " average ", average, " hex ", value)
	waitGroup := &sync.WaitGroup{}
	for {
		for i := 0; i < count; i++ {
			//main unlock account
			if from == i {
				continue
			}

			for j := 0; j < len(noBalance); j++ {
				if i == noBalance[j] {
					getBalance = true
					noBalance = append(noBalance[:j], noBalance[j+1:]...)
					break
				}
				getBalance = false
			}

			if !getBalance {
				continue
			}

			if getBalance {
				// get balance
				err := client.Call(&result, "etrue_getBalance", account[i], "latest")
				if err != nil {
					fmt.Println("etrue_getBalance Error:", err)
					msg <- false
					return false
				}
				balance := getBalanceValue(result)
				balanceTrue := new(big.Int).Set(balance)
				fmt.Println("etrue_getBalance son address ", account[i], " result ", balance, " i ", i, " true ", balanceTrue.Div(balanceTrue, big.NewInt(1000000000000000000)))
				if balance.Cmp(average) >= 0 {
					if i == count-1 && len(noBalance) == 0 {
						find = true
						break
					}
					continue
				} else {
					noBalance = append(noBalance, i)
				}
			}
			fmt.Println(i, " Transaction main address ", account[from], " son address ", account[i], " value ", value)
			waitGroup.Add(1)
			go sendTransaction(client, account[from], i, account[i], value, waitGroup)
		}

		waitGroup.Wait()
		time.Sleep(time.Second * 8)

		if find {
			break
		}
	}

	return true
}

func unlockCountNewAccount(client *rpc.Client, count int) bool {
	waitGroup := &sync.WaitGroup{}
	for i := 0; i < count; i++ {
		if from == i {
			continue
		}
		waitGroup.Add(1)

		unlockSonAccount(client, account[i], i, waitGroup)
	}
	waitGroup.Wait()
	return true
}

// unlockSonAccount
func unlockSonAccount(client *rpc.Client, account string, index int, wait *sync.WaitGroup) {
	defer wait.Done()
	fmt.Println("unlockAccount address index ", index, " son address ", account)
	_, err := unlockAccount(client, account, "admin", 9000000, "son address")
	if err != nil {
		fmt.Println("personal_unlockAccount Error:", err.Error(), " index ", index, "addr", account)
		msg <- false
	}
}
