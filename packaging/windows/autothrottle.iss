; The Windows installer, built with Inno Setup 6.3 or later:
;
;   iscc /DAppVersion=1.4.2 /DFileVersion=1.4.2.0 /DBinDir=C:\path\to\bin autothrottle.iss
;
; BinDir holds amd64\ and arm64\, each with autothrottle.exe and
; autothrottle-tray.exe (the Dockerfile's "windows" target). One installer
; carries both and installs the one the machine runs natively.
;
; It installs the program, registers the AutoThrottle service (which runs
; as LocalService), lets port 7676 through the firewall on private and
; domain networks, and starts the service. Running it again upgrades in
; place: settings and history live in ProgramData, which it never touches.
; Silent installs: /VERYSILENT. Silent uninstalls keep the data unless
; given /PURGE.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef FileVersion
  #define FileVersion "0.0.0.0"
#endif
#ifndef BinDir
  #define BinDir "bin"
#endif

#define Repo "https://github.com/JaiMistry/autothrottle"

[Setup]
; Never change: it is how an upgrade finds the installation it replaces.
AppId={{C3182708-4FA6-47B4-9A23-6615C3A0C72F}
AppName=AutoThrottle
AppVersion={#AppVersion}
AppVerName=AutoThrottle {#AppVersion}
AppPublisher=JaiMistry
AppPublisherURL={#Repo}
AppSupportURL={#Repo}/issues
AppUpdatesURL={#Repo}/releases
DefaultDirName={autopf}\AutoThrottle
DisableProgramGroupPage=yes
LicenseFile=..\..\LICENSE
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible or arm64
ArchitecturesInstallIn64BitMode=x64compatible or arm64
MinVersion=10.0
SetupIconFile=..\..\cmd\autothrottle-tray\autothrottle.ico
UninstallDisplayIcon={app}\autothrottle.exe
UninstallDisplayName=AutoThrottle
WizardStyle=modern
; The tray icon is closed and the service stopped before files are
; replaced (PrepareToInstall), so nothing is left for Windows to close.
CloseApplications=no
OutputDir=out
OutputBaseFilename=AutoThrottle-Setup-{#AppVersion}
Compression=lzma2/max
SolidCompression=yes
VersionInfoVersion={#FileVersion}
VersionInfoProductName=AutoThrottle
VersionInfoDescription=AutoThrottle Setup

[Tasks]
Name: trayicon; Description: "Show the AutoThrottle icon in the notification area"

[Files]
Source: "{#BinDir}\amd64\autothrottle.exe"; DestDir: "{app}"; Check: not IsArm64; Flags: ignoreversion
Source: "{#BinDir}\amd64\autothrottle-tray.exe"; DestDir: "{app}"; Check: not IsArm64; Flags: ignoreversion
Source: "{#BinDir}\arm64\autothrottle.exe"; DestDir: "{app}"; Check: IsArm64; Flags: ignoreversion
Source: "{#BinDir}\arm64\autothrottle-tray.exe"; DestDir: "{app}"; Check: IsArm64; Flags: ignoreversion
Source: "..\..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\AutoThrottle"; Filename: "{app}\autothrottle-tray.exe"; Parameters: "open"; Comment: "Open AutoThrottle in your browser"
Name: "{commonstartup}\AutoThrottle"; Filename: "{app}\autothrottle-tray.exe"; Tasks: trayicon

[Run]
Filename: "{app}\autothrottle-tray.exe"; Flags: nowait runasoriginaluser; Tasks: trayicon
Filename: "{app}\autothrottle-tray.exe"; Parameters: "open"; Description: "Open AutoThrottle"; Flags: postinstall nowait runasoriginaluser skipifsilent

[Code]
const
  FirewallRule = 'AutoThrottle';

// Run a program, wait for it, and log what it printed. Output is the
// printed text, for an error message.
function RunStep(const Exe, Params, What: String; var Output: String): Boolean;
var
  ResultCode: Integer;
  OutFile: String;
  Text: AnsiString;
begin
  OutFile := AddBackslash(GetTempDir) + 'autothrottle-setup-step.txt';
  DeleteFile(OutFile);
  Result := Exec(ExpandConstant('{cmd}'), '/C ""' + Exe + '" ' + Params + ' > "' + OutFile + '" 2>&1"',
    '', SW_HIDE, ewWaitUntilTerminated, ResultCode) and (ResultCode = 0);
  Output := '';
  if LoadStringFromFile(OutFile, Text) then
    Output := Trim(String(Text));
  DeleteFile(OutFile);
  Log(What + ' (exit ' + IntToStr(ResultCode) + '): ' + Output);
end;

procedure CloseTrayIcons;
var
  ResultCode: Integer;
begin
  Exec('taskkill.exe', '/F /IM autothrottle-tray.exe', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

function HasParam(const Name: String): Boolean;
var
  I: Integer;
begin
  Result := False;
  for I := 1 to ParamCount do
    if CompareText(ParamStr(I), Name) = 0 then
      Result := True;
end;

// An upgrade: stop what is running so its files can be replaced.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Exe, Output: String;
begin
  Result := '';
  CloseTrayIcons;
  Exe := ExpandConstant('{app}\autothrottle.exe');
  if FileExists(Exe) and not RunStep(Exe, 'service stop', 'Stopping the service', Output) then
    Result := 'The AutoThrottle service could not be stopped:' + #13#10#13#10 + Output +
      #13#10#13#10 + 'Stop it from Services, then run Setup again.';
end;

procedure Fail(const What, Output: String);
begin
  SuppressibleMsgBox('AutoThrottle was installed, but Setup could not ' + What + ':' + #13#10#13#10 + Output +
    #13#10#13#10 + 'Run Setup again, or open an issue at {#Repo}/issues.', mbError, MB_OK, IDOK);
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Exe, Output: String;
begin
  if CurStep <> ssPostInstall then
    exit;
  Exe := ExpandConstant('{app}\autothrottle.exe');
  if not RunStep(Exe, 'service install', 'Registering the service', Output) then begin
    Fail('register the AutoThrottle service', Output);
    exit;
  end;
  // Private and domain networks only: on a public network, such as a
  // cafe's Wi-Fi, nobody else gets to the page, or to its first-run
  // account screen.
  RunStep('netsh.exe', 'advfirewall firewall delete rule name="' + FirewallRule + '"', 'Removing an old firewall rule', Output);
  if not RunStep('netsh.exe', 'advfirewall firewall add rule name="' + FirewallRule + '" dir=in action=allow ' +
      'program="' + Exe + '" protocol=TCP localport=7676 profile=private,domain enable=yes', 'Adding the firewall rule', Output) then
    Fail('allow AutoThrottle through Windows Firewall', Output);
  if not RunStep(Exe, 'service start', 'Starting the service', Output) then
    Fail('start the AutoThrottle service', Output);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Output, DataDir: String;
begin
  if CurUninstallStep = usUninstall then begin
    CloseTrayIcons;
    if not RunStep(ExpandConstant('{app}\autothrottle.exe'), 'service uninstall', 'Removing the service', Output) then
      SuppressibleMsgBox('The AutoThrottle service could not be removed:' + #13#10#13#10 + Output, mbError, MB_OK, IDOK);
    RunStep('netsh.exe', 'advfirewall firewall delete rule name="' + FirewallRule + '"', 'Removing the firewall rule', Output);
  end;
  if CurUninstallStep = usPostUninstall then begin
    DataDir := ExpandConstant('{commonappdata}\AutoThrottle');
    if not DirExists(DataDir) then
      exit;
    if UninstallSilent then begin
      if HasParam('/PURGE') then
        DelTree(DataDir, True, True, True);
    end else if MsgBox('Also delete your AutoThrottle settings and history?' + #13#10#13#10 +
        'Choose No to keep them for a later reinstall. They are in ' + DataDir + '.',
        mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
      DelTree(DataDir, True, True, True);
  end;
end;
