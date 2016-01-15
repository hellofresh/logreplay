// logreplay project logreplay.go

package main

import (
	"bytes"
	"fmt"
	"github.com/hellofresh/logreplay/Godeps/_workspace/src/github.com/juju/deputy"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
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

func main() {
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
