package main

import (
	"github.com/CrimsonAS/qbackend/backend"
	"github.com/CrimsonAS/qbackend/backend/qmlscene"
)

type Root struct {
	qbackend.QObject
}

type Person struct {
	qbackend.QObject
	FirstName string
	LastName  string
	Age       int
}

type PersonModel struct {
	qbackend.Model
	people []*Person
	Count  int
}

func (this *PersonModel) Row(row int) interface{} {
	p := this.people[row]
	v := make([]interface{}, 3)
	v[0] = p.FirstName
	v[1] = p.LastName
	v[2] = p.Age
	return v
}

func (this *PersonModel) RowCount() int {
	return len(this.people)
}

func (this *PersonModel) RoleNames() []string {
	return []string{
		"firstName",
		"lastName",
		"age",
	}
}

func (this *PersonModel) AddPerson() {
	desiredAge := 0
	if len(this.people) > 0 {
		desiredAge = this.people[len(this.people)-1].Age + 1
	}
	p := &Person{FirstName: "John", LastName: "Brooks", Age: desiredAge}
	this.people = append(this.people, p)
	this.Inserted(len(this.people)-1, 1)
	this.Count = len(this.people)
	this.Changed("Count")
}

func (this *PersonModel) RemovePerson(idx int) {
	this.people = append(this.people[0:idx], this.people[idx+1:]...)
	this.Removed(idx, 1)
	this.Count = len(this.people)
	this.Changed("Count")
}

func (this *PersonModel) UpdatePerson(idx int, firstName, lastName string, age int) {
	p := this.people[idx]
	p.FirstName = firstName
	p.LastName = lastName
	p.Age = age
	this.Updated(idx)
}

func main() {
	qmlscene.Connection.RootObject = &Root{}
	qmlscene.Connection.RegisterType("PersonModel", &PersonModel{})
	qmlscene.Connection.RegisterType("Person", &Person{})
	qmlscene.RunFile("main.qml")
}
