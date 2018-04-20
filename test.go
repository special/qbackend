package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	qbackend "./qbackend_go"
	"github.com/satori/go.uuid"
)

type Person struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Age       int    `json:"age,string"`
}

var scanningForDataLength bool = false
var byteCnt int64

// dropCR drops a terminal \r from the data.
func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}

	return data
}

// Hack to make Scanner give us line by line data, or a block of byteCnt bytes.
func scanLinesOrBlock(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if scanningForDataLength {
		//fmt.Printf("DEBUG Want %d got %d\n", byteCnt, len(data))
		if len(data) < int(byteCnt) {
			return 0, nil, nil
		}

		return int(byteCnt), data, nil
	}

	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		// We have a full newline-terminated line.
		return i + 1, dropCR(data[0:i]), nil
	}

	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), dropCR(data), nil
	}

	// Request more data.
	return 0, nil, nil
}

type generalData struct {
	qbackend.Store
	TestData    string `json:"testData"`
	TotalPeople int    `json:"totalPeople"`
}

type PersonModel struct {
	qbackend.JsonModel
}

func main() {
	qbackend.Startup()

	gd := &generalData{TestData: "Now connected", TotalPeople: 0}
	gd.Publish("GeneralData")
	gd.Update(gd)

	pm := &PersonModel{}
	pm.JsonModel = qbackend.JsonModel{
		SetHook: func(uuid uuid.UUID, value interface{}) {
			_, ok := pm.Get(uuid)
			if ok {
				return
			} else {
				gd.TotalPeople++
				gd.Update(gd)
			}
		},
		RemoveHook: func(uuid uuid.UUID) {
			gd.TotalPeople--
			gd.Update(gd)
		},
	}
	pm.Publish("PersonModel")

	u, _ := uuid.NewV4()
	pm.Set(u, Person{FirstName: "Robin", LastName: "Burchell", Age: 31})
	u, _ = uuid.NewV4()
	pm.Set(u, Person{FirstName: "Kamilla", LastName: "Bremeraunet", Age: 30})

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Split(scanLinesOrBlock)

	for scanner.Scan() {
		line := scanner.Text()
		//fmt.Println(fmt.Sprintf("DEBUG %s", line))

		if strings.HasPrefix(line, "SUBSCRIBE ") {
			parts := strings.Split(line, " ")
			if parts[1] == "PersonModel" {
				pm.Subscribe()
			} else if parts[1] == "GeneralData" {
				gd.Subscribe()
			}
		} else if strings.HasPrefix(line, "INVOKE ") {
			parts := strings.Split(line, " ")
			if len(parts) < 4 {
				panic("too short")
				continue
			}

			// Read the JSON blob
			byteCnt, _ = strconv.ParseInt(parts[3], 10, 32)

			scanningForDataLength = true
			scanner.Scan()
			scanningForDataLength = false

			jsonBlob := []byte(scanner.Text())

			if parts[1] == "PersonModel" {
				if parts[2] == "addNew" {
					u, _ := uuid.NewV4()
					pm.Set(u, Person{FirstName: "Another", LastName: "Person", Age: 15 + pm.Length()})
				}

				// ### must be a model member for now
				if parts[2] == "remove" {
					type removeCommand struct {
						UUID uuid.UUID `json:"UUID"`
					}
					var removeCmd removeCommand
					json.Unmarshal(jsonBlob, &removeCmd)
					pm.Remove(removeCmd.UUID)
				} else if parts[2] == "update" {
					type updateCommand struct {
						UUID   uuid.UUID `json:"UUID"`
						Person Person    `json:"data"`
					}
					var updateCmd updateCommand
					err := json.Unmarshal([]byte(jsonBlob), &updateCmd)
					fmt.Printf("From blob %s, person is now %+v err %+v\n", jsonBlob, updateCmd.Person, err)
					pm.Set(updateCmd.UUID, updateCmd.Person)
				}
			}

			// Skip the JSON blob
		}
	}

	fmt.Printf("Quitting?\n")
}
