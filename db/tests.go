// Copyright © 2017 Maurizio Tomasi <maurizio.tomasi@unimi.it>
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package db

import (
	"fmt"
	"io"
	"path"
	"os"
	"time"

	"github.com/astrogo/fitsio"

	"github.com/ziotom78/stdb/convert"
)

// Test is a structure holding all the information related to a test
type Test struct {
	ShortName string // Short name of the test
	Description string // Full description of the test
	CreationDate time.Time // Time when the acquisition of data stopped
	Username string // Name of the user which has uploaded the test in the db
	FitsChecksum string // Checksum of the FITS file containing the test data
	TestType string // Type of test (it should refer to the Test Plan document)
	TimeSpanSec float32 // Length of the test, in seconds
	CryogenicFlag bool // Was the test performed at cryogenic temperatures?
	Polarimeter int // Number of the polarimeter being tested
	NumOfSamples int // Number of samples acquired during the test
}

// FileCopy copies the file with path "sourcePath" into the file with
// path "destPath". No soft/hard links are used, and ownership and
// permissions are not preserved.
func FileCopy(destPath, sourcePath string) error {
    in, err := os.Open(sourcePath)
    if err != nil { 
		return err
	}
    defer in.Close()

    out, err := os.Create(destPath)
    if err != nil { 
		return err
	}
    defer out.Close()

    _, err = io.Copy(out, in)
    cerr := out.Close()
    if err != nil { 
		return err
	}

    return cerr
}

func createFitsFile(inputFileName string, w io.Writer, test *Test) (convert.TestFile, error) {
	var result convert.TestFile

	// Determine the file type
	fileType, err := convert.FileType(inputFileName)
	if err != nil {
		return result, fmt.Errorf("unable to determine the type of file \"%s\": %v",
				                  inputFileName, err)
	}

	// From the file type, pick the function to be called in order to convert the file
	// into a FITS file
	convFunctions := map[string]func(string, io.Writer, []fitsio.Card) (convert.TestFile, error){
		"keithley": convert.KeithleyXlsToFits,
	}
	conversionFn, ok := convFunctions[fileType]
	if !ok {
		return result, fmt.Errorf("unsupported file type \"%s\", update stdb to the latest version",
					              fileType)
	}

	fitshdr := []fitsio.Card {
		{ Name: "shortnam", Value: test.ShortName, Comment: "Short name of the test" },
		{ Name: "creadate", Value: test.CreationDate, Comment: "Creation date (UTC)" },
		{ Name: "username", Value: test.Username, Comment: "Username of the uploader" },
		{ Name: "testtype", Value: test.TestType, Comment: "Type of the test" },
		{ Name: "cryo", Value: test.CryogenicFlag, Comment: "Was the test done at cryogenic temperatures?" },
		{ Name: "polarim", Value: test.Polarimeter, Comment: "Number of the polarimeter being tested" },
		{ Name: "stdbver", Value: DatabaseSchemaVersion, Comment: "Version of the database schema" },
	}

	// Create the FITS file
	return conversionFn(inputFileName, w, fitshdr)
}

// AddTest creates a new entry in the "tests" table of the database and
// fills it with the details of "newTest". The file "fitsFileName" is copied
// in the database folder, and it can therefore be removed after successful
// completion of this function. The return value contains the
// unique id of the test and an Error object.
func (conn *Connection) AddTest(newTest *Test,
                                username string,
								inputFileName string) (int64, error) {
	if (! conn.Active) {
		return -1, fmt.Errorf(MsgInactiveConnection)
	}

	newTest.Username = username

	tx, err := conn.Connection.Begin()
	if err != nil {
		return -1, err
	}

	result, err := tx.Exec(`
insert into tests (short_name, 
                   description, 
				   user_id, 
				   type, 
				   is_cryogenic,
				   polarimeter)
values (?, ?, ?, ?, ?, ?)`,
		newTest.ShortName,
		newTest.Description,
		username,
		newTest.TestType,
		newTest.CryogenicFlag,
		newTest.Polarimeter)
	if err != nil {
		tx.Rollback()
		return -1, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		tx.Rollback()
		return -1, err
	}

	outFitsFilePath := path.Join(conn.BasePath, fmt.Sprintf("test_%06d.fits.gz", id))
	outFits, err := os.Create(outFitsFilePath)
	if err != nil {
		tx.Rollback()
		return -1, err
	}

	testFile, err := createFitsFile(inputFileName, outFits, newTest)
	if err != nil {
		outFits.Close()
		os.Remove(outFitsFilePath)
		tx.Rollback()
		return -1, err
	}

	if err := outFits.Close(); err != nil {
		os.Remove(outFitsFilePath)
		tx.Rollback()
		return -1, err
	}

	// Update the entry in the database with the information extracted from
	// the FITS file that has just been created
	result, err = tx.Exec(`
update tests set (creation_date,
                  time_span_sec,
				  num_of_samples)
values (?, ?, ?)`,
		testFile.CreationDate.Format(time.RFC3339),
		testFile.TimeSpanSec,
		testFile.NumOfSamples)

	if err := tx.Commit(); err != nil {
		os.Remove(outFitsFilePath)
		return -1, err
	}

	return id, nil
}
