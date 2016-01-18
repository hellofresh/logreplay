// logreplay project logreplay.go

package main

import (
	"bytes"
	"fmt"
	"github.com/hellofresh/logreplay/Godeps/_workspace/src/github.com/docopt/docopt-go"
	"github.com/hellofresh/logreplay/Godeps/_workspace/src/github.com/juju/deputy"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"text/template"
	"time"
)

const (
	WS_ACCESS_KEY_ID      string = "AWS_ACCESS_KEY_ID"
	AWS_SECRET_ACCESS_KEY string = "AWS_SECRET_ACCESS_KEY"
	S3_BUCKET             string = "S3_BUCKET"
	LOGS_PATH             string = "LOGS_PATH"
	ES_TYPE               string = "ES_TYPE"
	ES_HOST               string = "ES_HOST"
	ES_INDEX              string = "ES_INDEX"
)

type Credentials struct {
	Key, Secret string
}

type FbeatCfg struct {
	// Starting after '/mnt/s3'.
	ProspectorPath string
	ESType         string
	ESHost         string
	ESIndex        string
}

// Fetches envionment variables; failing if specified environment variable could not be found or is empty string.
func getEnvVarOrFail(envVar string) string {
	var res string
	res = os.Getenv(envVar)
	if res == "" {
		log.Fatalf("Error: Missing and/or empty required environment variable '%s'.\n", envVar)
	}
	return res
}

// Reads template file tmplPth from disk, renders template with provided templateData and writes result
// to dstPth on disk.
// TODO Possible code smell: Too many things at once in this function.
func renderAndWriteTemplate(templateData interface{}, tmplPth, dstPth string, destPerm os.FileMode) error {
	// Read template file.
	tmplCnt, err := ioutil.ReadFile(tmplPth)
	if err != nil {
		log.Fatalf("Error: Unable to open template file '%s': %s\n", tmplPth, err.Error())
	}

	rnrdTmpl := new(bytes.Buffer)
	// Create template thingy from template file content.
	templ, err := template.New("cred").Parse(string(tmplCnt))
	if err != nil {
		fmt.Errorf("Error: Unable to instanciate template from template file '%s': \n", tmplPth, err.Error())
	}
	// Render template.
	err = templ.Execute(rnrdTmpl, templateData)
	if err != nil {
		fmt.Errorf("Error: Unable to render template: %s\n", err.Error())
	}
	// Write rendered template.
	err = ioutil.WriteFile(dstPth, rnrdTmpl.Bytes(), destPerm)
	if err != nil {
		fmt.Errorf("Error: Cannot write rendered template file '%s': %s\n", dstPth, err.Error())
	}
	return nil
}

// Mounting an S3 bucket under the mountpoint /mnt/s3 via s3fs-fuse.
func mountS3(bucket string) error {
	d := deputy.Deputy{
		Errors:    deputy.FromStderr,
		StdoutLog: func(b []byte) { log.Print(string(b)) },
		Timeout:   time.Second * 10,
	}

	cmd2Exc := exec.Command("s3fs", bucket, "/mnt/s3", "-o", "passwd_file=/root/aws_creds")

	if err := d.Run(cmd2Exc); err != nil {
		return err
	}
	return nil
}

// Loading the Filebeat index template into ElasticSearch as described in:
// 	 https://www.elastic.co/guide/en/beats/filebeat/current/filebeat-getting-started.html#filebeat-template
func loadFilebeatIndexTemplate(esHost string) error {
	d := deputy.Deputy{
		Errors:    deputy.FromStderr,
		StdoutLog: func(b []byte) { log.Print(string(b)) },
	}
	cmd2Exc := exec.Command("curl", "-XPUT", esHost+"/_template/filebeat?pretty",
		"-d@/etc/filebeat/filebeat.template.json")

	if err := d.Run(cmd2Exc); err != nil {
		return err
	}
	return nil
}

// Starting the Filebeat service using the config file /etc/filebeat/filebeat.yml.
func startFilebeatAgent() error {
	d := deputy.Deputy{
		Errors:    deputy.FromStderr,
		StdoutLog: func(b []byte) { log.Print(string(b)) },
	}
	/*
			Filebeat command line options explanations:
				(Reference: https://www.elastic.co/guide/en/beats/filebeat/current/filebeat-command-line.html)
			-e Log to stderr and disable syslog/file output.
			-v Enable verbose output to show INFO-level messages.

		The output of the filebeat agent will be visible in the Docker output due to redirecting STDERR to STDOUT
		in Bash shell command.
	*/
	cmd2Exc := exec.Command("/bin/bash", "-c", "/usr/bin/filebeat -v -e -c /etc/filebeat/filebeat.yml 2>&1")

	if err := d.Run(cmd2Exc); err != nil {
		return err
	}
	return nil
}

// In case --mount-only option is set, drop the user in an own shell process for looking at mounted S3 bucket data.
// This code was shamelessly copied from Matt Butcher's cool blog post: Start an Interactive Shell from Within Go
// (http://technosophos.com/2014/07/11/start-an-interactive-shell-from-within-go.html).

func spawnInteractiveLoginShell() error {
	dstDir := "/mnt/s3"
	if err := os.Chdir("/mnt/s3"); err != nil {
		return fmt.Errorf("Error: Cannot change working directory to '%s': %s\n", dstDir, err.Error())
	}

	// Get the current user.
	curUser, err := user.Current()
	if err != nil {
		return fmt.Errorf("Error: Cannot determine current user: %s\n", err.Error())
	}

	// Transfer stdin, stdout, and stderr to the new process. Also set target directory for the shell to start in.
	pa := os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Dir:   dstDir,
	}

	/*
		Start up a new shell.
		Used login options:
			-f
		        Do not perform authentication, user is preauthenticated.
		        Note: In that case, username is mandatory.
		    -p
		        Preserve environment.

		Reference: http://manpages.ubuntu.com/manpages/trusty/man1/login.1.html
	*/
	log.Printf("About to start a new interactive shell.")
	proc, err := os.StartProcess("/bin/login", []string{"login", "-p", "-f", curUser.Username}, &pa)
	if err != nil {
		return fmt.Errorf("Error: Unable to start new interactive login shell: %s\n", err.Error())
	}

	// Wait until user exits the shell
	state, err := proc.Wait()
	if err != nil {
		return fmt.Errorf("Error: Something went wrong with the shell process: %s\n", err.Error())
	}

	// Keep on keepin' on.
	log.Printf("Successfully quit interactive login shell for user '%s' with state: %s\n", curUser.Username,
		state.String())

	return nil
}

func main() {
	// Parsing command line arguments.
	usage := `
replay logs.

Usage:
  logreplay replay [--mount-only]
  logreplay -h | --help

Options:
--mount-only   Do not start Filebeat service, only mount S3 bucket; start shell to look around [default: false].
-h --help      Show this screen.`

	// Do not require options first (reference: https://github.com/docopt/docopt.go/blob/master/docopt.go#L45).
	args, err := docopt.Parse(usage, os.Args[1:], true, "", false)
	if err != nil {
		log.Fatalf("Error: Unable to parse command line arguments: %s", err.Error())
	}

	// AWS Credentials file for s3fs-fuse.
	creds := Credentials{
		Key:    getEnvVarOrFail(WS_ACCESS_KEY_ID),
		Secret: getEnvVarOrFail(AWS_SECRET_ACCESS_KEY),
	}

	credsTmplFile := "template/aws_creds.template"
	credsDstFile := "/root/aws_creds"

	if err := renderAndWriteTemplate(creds, credsTmplFile, credsDstFile, 0600); err != nil {
		log.Fatalln(err.Error())
	}

	// Mount the S3 bucket.
	bkt2Mnt := getEnvVarOrFail(S3_BUCKET)
	if err := mountS3(bkt2Mnt); err != nil {
		log.Fatalf("Error: Could not mount S3 bucket '%s': %s\n", bkt2Mnt, err)
	}
	log.Printf("Successfully mounted S3 bucket '%s'.\n", bkt2Mnt)

	// Delete the AWS credentials file again. So in case the changed image is not removed
	// (forgetting 'docker run --rm ...'), the credentials are not contained anymore in the image itself.
	if err := os.Remove(credsDstFile); err != nil {
		log.Fatalf("Error: Unable to delete AWS creds file after mounting S3 bucket: %s\n", err.Error())
	}
	log.Printf("Successfully deleted credentials file.\n")

	// Check if mounting the S3 bucket is all we have to do.
	if args["--mount-only"].(bool) {
		if err := spawnInteractiveLoginShell(); err != nil {
			log.Printf(err.Error())
		}
		// Halt programm execution here for --mount-only option.
		os.Exit(0)
	}

	// FileBeat configuration.
	fBeatCfg := FbeatCfg{
		ProspectorPath: getEnvVarOrFail(LOGS_PATH),
		ESType:         getEnvVarOrFail(ES_TYPE),
		ESHost:         getEnvVarOrFail(ES_HOST),
		ESIndex:        getEnvVarOrFail(ES_INDEX),
	}

	fBeatTmplFile := "template/filebeat.yml.template"
	fBeatDstFile := "/etc/filebeat/filebeat.yml"

	if err := renderAndWriteTemplate(fBeatCfg, fBeatTmplFile, fBeatDstFile, 0644); err != nil {
		log.Fatalln(err.Error())
	}

	// Load index template into ElasticSearch.
	if err := loadFilebeatIndexTemplate(fBeatCfg.ESHost); err != nil {
		log.Fatalf("Error: Cannot load Filebeat index template into ElasticSearch: %s\n", err.Error())
	}
	log.Printf("Successfully loaded Filebeat index template into ElasticSearch.\n")

	//	Start filebeat service to feed the logs into ES.
	if err := startFilebeatAgent(); err != nil {
		log.Fatalf("Error: Unable to start filebeat agent: %s\n", err.Error())
	}
}
