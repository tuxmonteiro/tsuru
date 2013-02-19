// Copyright 2013 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"fmt"
	"github.com/globocom/tsuru/db"
	"github.com/globocom/tsuru/log"
	"github.com/globocom/tsuru/provision"
	"github.com/globocom/tsuru/queue"
	"github.com/globocom/tsuru/service"
	"io/ioutil"
	"labix.org/v2/mgo/bson"
)

const (
	// queue actions
	regenerateApprc         = "regenerate-apprc"
	startApp                = "start-app"
	RegenerateApprcAndStart = "regenerate-apprc-start-app"
	bindService             = "bind-service"

	// name of the queue for internal messages.
	queueName = "tsuru-app"

	// Name of the queue for external messages.
	QueueName = "tsuru-app-public"
)

// ensureAppIsStarted make sure that the app and all units present in the given
// message are started.
func ensureAppIsStarted(msg *queue.Message) (App, error) {
	a := App{Name: msg.Args[0]}
	err := a.Get()
	if err != nil {
		return a, fmt.Errorf("Error handling %q: app %q does not exist.", msg.Action, a.Name)
	}
	units := getUnits(&a, msg.Args[1:])
	if !a.Available() || !units.Started() {
		format := "Error handling %q for the app %q:"
		uState := units.State()
		if uState == "error" || uState == "down" {
			format += fmt.Sprintf(" units are in %q state.", uState)
			msg.Delete()
		} else {
			format += " all units must be started."
		}
		return a, fmt.Errorf(format, msg.Action, a.Name)
	}
	return a, nil
}

// bindUnit handles the bind-service message, binding a unit to all service
// instances binded to the app.
func bindUnit(msg *queue.Message) error {
	a := App{Name: msg.Args[0]}
	err := a.Get()
	if err != nil {
		msg.Delete()
		return fmt.Errorf("Error handling %q: app %q does not exist.", msg.Action, a.Name)
	}
	conn, err := db.Conn()
	if err != nil {
		return fmt.Errorf("Error handling %q: %s", msg.Action, err)
	}
	unit := getUnits(&a, msg.Args[1:])[0]
	var instances []service.ServiceInstance
	q := bson.M{"apps": bson.M{"$in": []string{msg.Args[0]}}}
	err = conn.ServiceInstances().Find(q).All(&instances)
	if err != nil {
		return err
	}
	for _, instance := range instances {
		_, err = instance.BindUnit(&unit)
		if err != nil {
			log.Printf("Error binding the unit %s with the service instance %s.", unit.Name, instance.Name)
		}
	}
	return nil
}

// handle is the function called by the queue handler on each message.
func handle(msg *queue.Message) {
	switch msg.Action {
	case RegenerateApprcAndStart:
		fallthrough
	case regenerateApprc:
		if len(msg.Args) < 1 {
			log.Printf("Error handling %q: this action requires at least 1 argument.", msg.Action)
			return
		}
		app, err := ensureAppIsStarted(msg)
		if err != nil {
			log.Print(err)
			return
		}
		msg.Delete()
		app.serializeEnvVars()
		fallthrough
	case startApp:
		if msg.Action == regenerateApprc {
			break
		}
		if len(msg.Args) < 1 {
			log.Printf("Error handling %q: this action requires at least 1 argument.", msg.Action)
		}
		app, err := ensureAppIsStarted(msg)
		if err != nil {
			log.Print(err)
			return
		}
		err = app.Restart(ioutil.Discard)
		if err != nil {
			log.Printf("Error handling %q. App failed to start:\n%s.", msg.Action, err)
			return
		}
		msg.Delete()
	case bindService:
		err := bindUnit(msg)
		if err != nil {
			log.Print(err)
			return
		}
		msg.Delete()
	default:
		log.Printf("Error handling %q: invalid action.", msg.Action)
		msg.Delete()
	}
}

// unitList is a simple slice of units, with special methods to handle state.
type unitList []Unit

// Started returns true if all units in the list is started.
func (l unitList) Started() bool {
	for _, unit := range l {
		if unit.State != string(provision.StatusStarted) {
			return false
		}
	}
	return true
}

// State returns a string if all units have the same state. Otherwise it
// returns an empty string.
func (l unitList) State() string {
	if len(l) == 0 {
		return ""
	}
	state := l[0].State
	for i := 1; i < len(l); i++ {
		if l[i].State != state {
			return ""
		}
	}
	return state
}

// getUnits builds a unitList from the given app and the names in the string
// slice.
func getUnits(a *App, names []string) unitList {
	var units []Unit
	if len(names) > 0 {
		units = make([]Unit, len(names))
		i := 0
		for _, unitName := range names {
			for _, appUnit := range a.Units {
				if appUnit.Name == unitName {
					units[i] = appUnit
					i++
					break
				}
			}
		}
	}
	return unitList(units)
}

var (
	qfactory queue.QFactory
	_queue   queue.Q
	_handler queue.Handler
)

func handler() queue.Handler {
	if _handler != nil {
		return _handler
	}
	var err error
	if qfactory == nil {
		qfactory, err = queue.Factory()
		if err != nil {
			log.Fatalf("Failed to get the queue instance: %s", err)
		}
	}
	_handler, err = qfactory.Handler(handle, queueName, QueueName)
	if err != nil {
		log.Fatalf("Failed to create the queue handler: %s", err)
	}
	return _handler
}

func aqueue() queue.Q {
	if _queue != nil {
		return _queue
	}
	var err error
	if qfactory == nil {
		qfactory, err = queue.Factory()
		if err != nil {
			log.Fatalf("Failed to get the queue instance: %s", err)
		}
	}
	_queue, err = qfactory.Get(queueName)
	if err != nil {
		log.Fatalf("Failed to get the queue: %s", err)
	}
	return _queue
}

// enqueues the given messages and start the handler.
func enqueue(msgs ...queue.Message) {
	for _, msg := range msgs {
		copy := msg
		aqueue().Put(&copy, 0)
	}
	handler().Start()
}
