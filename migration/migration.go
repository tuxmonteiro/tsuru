// Copyright 2016 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package migration provides a "micro-framework" for migration management:
// each migration is a simple function that returns an error. All migration
// functions are executed in the order they were registered.
package migration

import (
	"errors"
	"fmt"
	"io"

	"github.com/tsuru/tsuru/db"
	"github.com/tsuru/tsuru/db/storage"
	"gopkg.in/mgo.v2/bson"
)

// ErrDuplicateMigration is the error returned by Register when the given name
// is already in use.
var ErrDuplicateMigration = errors.New("there's already a migration with this name")

// ErrMigrationNotFound is the error returned by RunOptional when the given
// name is not a registered migration.
var ErrMigrationNotFound = errors.New("migration not found")

// ErrMigrationMandatory is the error returned by Run when the given name is
// not an optional migration. It should be executed calling Run.
var ErrMigrationMandatory = errors.New("migration is mandatory")

// ErrMigrationAlreadyExecuted is the error returned by Run when the given
// name was previously executed and the force parameter was not supplied.
var ErrMigrationAlreadyExecuted = errors.New("migration already executed")

// ErrCannotForceMandatory is the error returned by Run when the force
// parameter is supplied without the name of a migration to run.
var ErrCannotForceMandatory = errors.New("mandatory migrations can only run once")

// MigrateFunc represents a migration function, that can be registered with the
// Register function. Migrations are later ran in the registration order, and
// this package keeps track of which migrate have ran already.
type MigrateFunc func() error

// RunArgs is used by Run and RunOptional functions to modify how migrations
// are executed.
type RunArgs struct {
	Name   string
	Writer io.Writer
	Dry    bool
	Force  bool
}

type migration struct {
	Name     string
	Ran      bool
	Optional bool
	fn       MigrateFunc
}

var migrations []migration

// Register register a new migration for later execution with the Run
// functions.
func Register(name string, fn MigrateFunc) error {
	return register(name, false, fn)
}

// RegisterOptional register a new migration that will not run automatically
// when calling the Run funcition.
func RegisterOptional(name string, fn MigrateFunc) error {
	return register(name, true, fn)
}

func register(name string, optional bool, fn MigrateFunc) error {
	for _, m := range migrations {
		if m.Name == name {
			return ErrDuplicateMigration
		}
	}
	migrations = append(migrations, migration{Name: name, Optional: optional, fn: fn})
	return nil
}

// Run runs all registered non optional migrations if no ".Name" is informed.
// Migrations are executed in the order that they were registered. If ".Name"
// is informed, an optional migration with the given name is executed.
func Run(args RunArgs) error {
	if args.Name != "" {
		return runOptional(args)
	}
	if args.Force {
		return ErrCannotForceMandatory
	}
	return run(args)
}

func run(args RunArgs) error {
	migrationsToRun, err := getMigrations(true)
	if err != nil {
		return err
	}
	coll, err := collection()
	if err != nil {
		return err
	}
	defer coll.Close()
	for _, m := range migrationsToRun {
		if m.Optional {
			continue
		}
		fmt.Fprintf(args.Writer, "Running %q... ", m.Name)
		if !args.Dry {
			err := m.fn()
			if err != nil {
				return err
			}
			m.Ran = true
			err = coll.Insert(m)
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(args.Writer, "OK")
	}
	return nil
}

func runOptional(args RunArgs) error {
	migrationsToRun, err := getMigrations(false)
	if err != nil {
		return err
	}
	var toRun *migration
	for i, m := range migrationsToRun {
		if m.Name == args.Name {
			toRun = &migrationsToRun[i]
			break
		}
	}
	if toRun == nil {
		return ErrMigrationNotFound
	}
	if !toRun.Optional {
		return ErrMigrationMandatory
	}
	if toRun.Ran && !args.Force {
		return ErrMigrationAlreadyExecuted
	}
	fmt.Fprintf(args.Writer, "Running %q... ", toRun.Name)
	if !args.Dry {
		coll, err := collection()
		if err != nil {
			return err
		}
		defer coll.Close()
		err = toRun.fn()
		if err != nil {
			return err
		}
		toRun.Ran = true
		_, err = coll.Upsert(bson.M{"name": toRun.Name}, toRun)
		if err != nil {
			return err
		}
	}
	fmt.Fprintln(args.Writer, "OK")
	return nil
}

func List() ([]migration, error) {
	return getMigrations(false)
}

func getMigrations(ignoreRan bool) ([]migration, error) {
	coll, err := collection()
	if err != nil {
		return nil, err
	}
	defer coll.Close()
	result := make([]migration, 0, len(migrations))
	names := make([]string, len(migrations))
	for i, m := range migrations {
		names[i] = m.Name
	}
	query := bson.M{"name": bson.M{"$in": names}, "ran": true}
	var ran []migration
	err = coll.Find(query).All(&ran)
	if err != nil {
		return nil, err
	}
	for _, m := range migrations {
		m.Ran = false
		for _, r := range ran {
			if r.Name == m.Name {
				m.Ran = true
				break
			}
		}
		if !ignoreRan || !m.Ran {
			result = append(result, m)
		}
	}
	return result, nil
}

func collection() (*storage.Collection, error) {
	conn, err := db.Conn()
	if err != nil {
		return nil, err
	}
	return conn.Collection("migrations"), nil
}
