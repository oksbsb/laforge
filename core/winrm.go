package core

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"text/template"

	"github.com/juju/utils/filepath"

	"github.com/pkg/errors"

	"github.com/masterzen/winrm"
)

// WinRMClient is a type to connection to Windows hosts remotely over the WinRM protocol
type WinRMClient struct {
	Config *WinRMAuthConfig
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Kind implements the Sheller interface
func (w *WinRMClient) Kind() string {
	return "winrm"
}

// SetIO implements the Sheller interface
func (w *WinRMClient) SetIO(stdout io.Writer, stderr io.Writer, stdin io.Reader) error {
	w.Stdin = stdin
	w.Stdout = stdout
	w.Stderr = stderr
	return nil
}

// SetConfig implements the Sheller interface
func (w *WinRMClient) SetConfig(c *WinRMAuthConfig) error {
	if c == nil {
		return errors.New("nil auth config provised")
	}
	w.Config = c
	return nil
}

// LaunchInteractiveShell implements the Sheller interface
func (w *WinRMClient) LaunchInteractiveShell() error {
	endpoint := winrm.NewEndpoint(
		w.Config.RemoteAddr,
		w.Config.Port,
		w.Config.HTTPS,
		w.Config.SkipVerify,
		[]byte{},
		[]byte{},
		[]byte{},
		0,
	)

	if w.Stderr == nil {
		w.Stderr = os.Stderr
	}

	if w.Stdin == nil {
		w.Stdin = os.Stdin
	}

	if w.Stdout == nil {
		w.Stdout = os.Stdout
	}

	params := winrm.DefaultParameters
	params.Timeout = "PT24H"
	client, err := winrm.NewClientWithParameters(endpoint, w.Config.User, w.Config.Password, params)
	if err != nil {
		return errors.WithMessage(err, "could not create winrm client")
	}

	shell, err := client.CreateShell()
	if err != nil {
		panic(err)
	}
	var cmd *winrm.Command
	cmd, err = shell.Execute("powershell -NoProfile -ExecutionPolicy Bypass")
	if err != nil {
		panic(err)
	}

	go io.Copy(cmd.Stdin, os.Stdin)
	go io.Copy(os.Stdout, cmd.Stdout)
	go io.Copy(os.Stderr, cmd.Stderr)

	cmd.Wait()
	shell.Close()

	return nil
}

// ExecuteNonInteractive allows you to execute commands in a non-interactive session (note: standard command shell, not powershell)
func (w *WinRMClient) ExecuteNonInteractive(cmd *RemoteCommand) error {
	endpoint := winrm.NewEndpoint(
		w.Config.RemoteAddr,
		w.Config.Port,
		w.Config.HTTPS,
		w.Config.SkipVerify,
		nil,
		nil,
		nil,
		0,
	)

	params := winrm.DefaultParameters
	params.Timeout = "PT12M"
	client, err := winrm.NewClientWithParameters(endpoint, w.Config.User, w.Config.Password, params)
	if err != nil {
		panic(err)
	}

	shell, err := client.CreateShell()
	if err != nil {
		log.Printf("[ERROR] error creating shell: %s", err)
		return err
	}

	err = shell.Close()
	if err != nil {
		log.Printf("[ERROR] error closing shell: %s", err)
		return err
	}

	winfp, err := filepath.NewRenderer("windows")
	if err != nil {
		panic(err)
	}

	if winfp.Ext(cmd.Command) == `.ps1` && !strings.Contains(cmd.Command, " ") {
		cmdstrbuf := new(bytes.Buffer)
		err = elevatedCommandTemplate.Execute(cmdstrbuf, struct{ Path string }{
			Path: cmd.Command,
		})
		if err != nil {
			return err
		}

		escp := new(bytes.Buffer)
		err = xml.EscapeText(escp, cmdstrbuf.Bytes())
		if err != nil {
			return err
		}

		eo := elevatedOptions{
			User:              w.Config.User,
			Password:          w.Config.Password,
			TaskName:          winfp.Base(cmd.Command),
			LogFile:           fmt.Sprintf("%s.log", cmd.Command),
			TaskDescription:   "running laforge command",
			XMLEscapedCommand: escp.String(),
		}

		outbuf := new(bytes.Buffer)
		err = elevatedTemplate.Execute(outbuf, eo)
		if err != nil {
			return err
		}

		encoded := Powershell(outbuf.String())
		cmd.Command = fmt.Sprintf("powershell -NoProfile -ExecutionPolicy Bypass -EncodedCommand %s", encoded)
	}

	status, err := client.Run(cmd.Command, cmd.Stdout, cmd.Stderr)
	cmd.SetExitStatus(status, err)

	// return nil

	// go io.Copy(wcmd.Stdin, stdin)
	// go io.Copy(cmd.Stdout, wcmd.Stdout)
	// go io.Copy(cmd.Stderr, wcmd.Stderr)

	// err = cmd.Wait()
	// if err != nil {
	// 	panic(err)
	// }

	// err = shell.Close()
	// if err != nil {
	// 	panic(err)
	// }

	// status, err := client.Run(cmd.Command, cmd.Stdout, cmd.Stderr)

	// wcmd, err = shell.Execute(cmd.Command)
	// if err != nil {
	// 	panic(err)
	// }

	// if cmd.Stdin != nil {
	// 	go io.Copy(wcmd.Stdin, cmd.Stdin)
	// }

	// go io.Copy(cmd.Stdout, wcmd.Stdout)
	// go io.Copy(cmd.Stderr, wcmd.Stderr)

	// wcmd.Wait()
	// cmderr := wcmd.Close()
	// exitStatus := wcmd.ExitCode()

	// cmd.SetExitStatus(status, err)
	// if err != nil {
	// 	panic(err)
	// }
	return nil
}

type elevatedOptions struct {
	User              string
	Password          string
	TaskName          string
	TaskDescription   string
	LogFile           string
	XMLEscapedCommand string
}

// Powershell wraps a PowerShell script
// and prepares it for execution by the winrm client
func Powershell(psCmd string) string {
	// 2 byte chars to make PowerShell happy
	wideCmd := ""
	for _, b := range []byte(psCmd) {
		wideCmd += string(b) + "\x00"
	}

	// Base64 encode the command
	input := []uint8(wideCmd)
	encodedCmd := base64.StdEncoding.EncodeToString(input)

	// Create the powershell.exe command line to execute the script
	return fmt.Sprintf("%s", encodedCmd)
}

var elevatedCommandTemplate = template.Must(template.New("ElevatedCommandRunner").Parse(`powershell -noprofile -executionpolicy bypass "& { if (Test-Path variable:global:ProgressPreference){set-variable -name variable:global:ProgressPreference -value 'SilentlyContinue'}; &'{{.Path}}'; exit $LastExitCode }"`))

var elevatedTemplate = template.Must(template.New("ElevatedCommand").Parse(`
$name = "{{.TaskName}}"
$log = [System.Environment]::ExpandEnvironmentVariables("{{.LogFile}}")
$s = New-Object -ComObject "Schedule.Service"
$s.Connect()
$t = $s.NewTask($null)
$t.XmlText = @'
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
	<Description>{{.TaskDescription}}</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <UserId>{{.User}}</UserId>
      <LogonType>Password</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT24H</ExecutionTimeLimit>
    <Priority>4</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>cmd</Command>
      <Arguments>/c {{.XMLEscapedCommand}}</Arguments>
    </Exec>
  </Actions>
</Task>
'@
if (Test-Path variable:global:ProgressPreference){$ProgressPreference="SilentlyContinue"}
$f = $s.GetFolder("\")
$f.RegisterTaskDefinition($name, $t, 6, "{{.User}}", "{{.Password}}", 1, $null) | Out-Null
$t = $f.GetTask("\$name")
$t.Run($null) | Out-Null
$timeout = 10
$sec = 0
while ((!($t.state -eq 4)) -and ($sec -lt $timeout)) {
  Start-Sleep -s 1
  $sec++
}
$line = 0
do {
  Start-Sleep -m 100
  if (Test-Path $log) {
    Get-Content $log | select -skip $line | ForEach {
      $line += 1
      Write-Output "$_"
    }
  }
} while (!($t.state -eq 3))
$result = $t.LastTaskResult
if (Test-Path $log) {
    Remove-Item $log -Force -ErrorAction SilentlyContinue | Out-Null
}
[System.Runtime.Interopservices.Marshal]::ReleaseComObject($s) | Out-Null
exit $result`))
