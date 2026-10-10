; Instalador gráfico del agente (Inno Setup 6). Lo compila build.ps1:
;   ISCC /DVersion=0.7.0 [/DBeta] instalador\syncronize-agent.iss
; Deja dist\Syncronize-Agent-Setup-<versión>.exe (o ...-beta-<versión>.exe).
;
; El instalador solo pone el .exe en Program Files y lo llama, sin consola:
;   enroll --codigo X --soporte-remoto si|no --resultado <archivo>
;   actualizar --resultado <archivo>
;   vinculos --resultado <archivo>
;   soporte permitir | quitar
; Toda la lógica (vincular, servicio, RustDesk) vive en el agente.

#ifndef Version
  #define Version "0.0.0"
#endif
#ifdef Beta
  #define Exe "syncronize-agent-beta.exe"
  #define Nombre "Syncronize Agent (BETA)"
  #define Salida "Syncronize-Agent-Setup-beta-" + Version
  #define Icono "icono-beta.ico"
#else
  #define Exe "syncronize-agent.exe"
  #define Nombre "Syncronize Agent"
  #define Salida "Syncronize-Agent-Setup-" + Version
  #define Icono "icono.ico"
#endif

[Setup]
; El mismo AppId para beta y prod: es un solo agente por equipo.
AppId={{8F3D2A61-6C1B-4E0B-9B7E-3C5A1F2D9E47}
AppName={#Nombre}
AppVersion={#Version}
AppVerName={#Nombre} {#Version}
AppPublisher=Syncronize SAC
AppPublisherURL=https://syncronize.net.pe
AppSupportURL=https://syncronize.net.pe/descargas
; Donde el agente espera estar (dirInstalacion en instalacion.go).
DefaultDirName={autopf}\Syncronize\Agent
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableWelcomePage=no
DisableReadyPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir=..\dist
OutputBaseFilename={#Salida}
SetupIconFile={#Icono}
UninstallDisplayIcon={app}\syncronize-agent.exe
UninstallDisplayName={#Nombre}
WizardStyle=modern
WizardImageFile=lateral.bmp,lateral@2x.bmp
WizardSmallImageFile=chico.bmp,chico@2x.bmp
Compression=lzma2
SolidCompression=yes
; El servicio lo detiene PrepareToInstall; nada más que cerrar.
CloseApplications=no
RestartApplications=no
VersionInfoVersion={#Version}.0
VersionInfoCompany=Syncronize SAC
VersionInfoDescription=Instalador de {#Nombre}
VersionInfoProductName={#Nombre}

[Languages]
Name: "es"; MessagesFile: "compiler:Languages\Spanish.isl"

[Messages]
WelcomeLabel1=Agente de monitoreo de Syncronize
WelcomeLabel2=Tu servicio técnico lo usa para revisar el estado de este equipo (discos, batería, memoria, antivirus e impresoras) y avisarte antes de que algo falle.%n%nEl agente NO lee tus archivos, fotos, contraseñas ni historial.%n%nTen a mano el código de vinculación que te dio el técnico.
ButtonNext=&Siguiente >
ButtonInstall=&Instalar
ButtonFinish=&Finalizar

[Files]
Source: "..\dist\{#Exe}"; DestDir: "{app}"; DestName: "syncronize-agent.exe"; Flags: ignoreversion

[UninstallRun]
; Quita el servicio, los vínculos y RustDesk (si lo puso el agente). Los
; archivos de Program Files los borra el desinstalador.
Filename: "{app}\syncronize-agent.exe"; Parameters: "uninstall --conservar-archivos"; Flags: runhidden waituntilterminated; RunOnceId: "QuitarAgente"

[Code]
var
  YaVinculado: Boolean;
  SoporteActual: Boolean;
  SoporteFijado: Boolean;
  ListaVinculos: String;
  PaginaModo: TInputOptionWizardPage;
  PaginaCodigo: TInputQueryWizardPage;
  PaginaSoporte: TInputOptionWizardPage;
  Resumen: String;
  Fallo: Boolean;

function ExeInstalado: String;
begin
  Result := ExpandConstant('{app}\syncronize-agent.exe');
end;

{ Lee con quién está vinculado el equipo (si ya tiene el agente). Usa el .exe
  de este instalador: el instalado puede ser una versión sin "vinculos". }
procedure LeerVinculos;
var
  rc, i, tab: Integer;
  archivo, linea, empresa, soporte: String;
  lineas: TArrayOfString;
begin
  YaVinculado := False;
  SoporteActual := False;
  ListaVinculos := '';
  if not FileExists(ExpandConstant('{commonappdata}\Syncronize\Agent\config.json')) then
    exit;
  { Se busca por el nombre con que se instala (DestName), no el de origen. }
  ExtractTemporaryFile('syncronize-agent.exe');
  archivo := ExpandConstant('{tmp}\vinculos.txt');
  if not Exec(ExpandConstant('{tmp}\syncronize-agent.exe'), 'vinculos --resultado "' + archivo + '"', '',
    SW_HIDE, ewWaitUntilTerminated, rc) then
    exit;
  if not LoadStringsFromFile(archivo, lineas) then
    exit;
  for i := 0 to GetArrayLength(lineas) - 1 do
  begin
    linea := Trim(lineas[i]);
    tab := Pos(#9, linea);
    if tab = 0 then
      continue;
    empresa := Copy(linea, 1, tab - 1);
    soporte := Copy(linea, tab + 1, 10);
    ListaVinculos := ListaVinculos + '      •  ' + empresa + #13#10;
    if soporte = 'si' then
      SoporteActual := True;
    YaVinculado := True;
  end;
end;

function Actualizando: Boolean;
begin
  Result := YaVinculado and (PaginaModo.SelectedValueIndex = 0);
end;

procedure InitializeWizard;
var
  despues: Integer;
begin
  LeerVinculos;
  despues := wpWelcome;
  if YaVinculado then
  begin
    PaginaModo := CreateInputOptionPage(wpWelcome,
      'Este equipo ya tiene el agente',
      '¿Qué quieres hacer?',
      'Este equipo está vinculado con:' + #13#10 + ListaVinculos + #13#10 + 'Elige una opción y haz clic en Siguiente.',
      True, False);
    PaginaModo.Add('Actualizar el agente a la versión {#Version}');
    PaginaModo.Add('Agregar OTRO servicio técnico (necesitas su código de vinculación)');
    PaginaModo.SelectedValueIndex := 0;
    despues := PaginaModo.ID;
  end;

  PaginaCodigo := CreateInputQueryPage(despues,
    'Código de vinculación',
    'Lo genera tu servicio técnico en Syncronize.',
    'Escribe el código que te dio el técnico (8 letras y números). Con él, este equipo queda vinculado con su servicio técnico.');
  PaginaCodigo.Add('Código:', False);
  PaginaCodigo.Edits[0].MaxLength := 20;

  PaginaSoporte := CreateInputOptionPage(PaginaCodigo.ID,
    'Soporte remoto (opcional)',
    '¿Permites que tu servicio técnico te ayude a distancia?',
    'Si lo permites, el técnico podrá ver y usar esta pantalla para ayudarte:' + #13#10#13#10 +
    '•  Cada vez que quiera conectarse aparecerá una ventana y TÚ decides si aceptas. Sin tu clic no puede entrar.' + #13#10#13#10 +
    '•  Solo se enciende cuando el técnico lo pide y se apaga al terminar (o a las 2 horas).' + #13#10#13#10 +
    '•  Usa RustDesk, un programa de código abierto que se descarga e instala aparte (unos 26 MB).' + #13#10#13#10 +
    '•  Puedes quitarlo cuando quieras volviendo a abrir este instalador.',
    False, False);
  PaginaSoporte.Add('Sí, permito el soporte remoto');
  PaginaSoporte.Values[0] := False;
end;

function ShouldSkipPage(PageID: Integer): Boolean;
begin
  Result := (PageID = PaginaCodigo.ID) and Actualizando;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID = PaginaSoporte.ID then
  begin
    if Actualizando then
    begin
      PaginaSoporte.CheckListBox.ItemCaption[0] := 'Sí, permito el soporte remoto a mis servicios técnicos';
      if not SoporteFijado then
        PaginaSoporte.Values[0] := SoporteActual;
      SoporteFijado := True;
    end
    else
      PaginaSoporte.CheckListBox.ItemCaption[0] := 'Sí, permito el soporte remoto';
  end;

  if CurPageID = wpFinished then
  begin
    if Fallo then
      WizardForm.FinishedHeadingLabel.Caption := 'No se pudo completar'
    else
      WizardForm.FinishedHeadingLabel.Caption := 'Listo';
    WizardForm.FinishedLabel.Caption := Resumen + #13#10#13#10 + 'Ya puedes cerrar esta ventana.';
  end;
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  c: String;
begin
  Result := True;
  if CurPageID = PaginaCodigo.ID then
  begin
    c := Uppercase(Trim(PaginaCodigo.Values[0]));
    StringChangeEx(c, ' ', '', True);
    StringChangeEx(c, '-', '', True);
    if Length(c) < 6 then
    begin
      MsgBox('Escribe el código que te dio el técnico (8 letras y números).', mbError, MB_OK);
      Result := False;
      exit;
    end;
    PaginaCodigo.Values[0] := c;
  end;
end;

{ Para el servicio antes de reemplazar el .exe. Si no existe, no pasa nada. }
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  rc: Integer;
begin
  Exec(ExpandConstant('{sys}\net.exe'), 'stop SyncronizeAgent', '', SW_HIDE, ewWaitUntilTerminated, rc);
  Sleep(1500);
  Result := '';
end;

{ Corre el agente instalado con --resultado y devuelve si terminó bien; el
  mensaje para el cliente queda en mensaje. }
function Correr(const params: String; var mensaje: String): Boolean;
var
  rc, i: Integer;
  archivo: String;
  lineas: TArrayOfString;
begin
  Result := False;
  mensaje := '';
  archivo := ExpandConstant('{tmp}\resultado.txt');
  DeleteFile(archivo);
  if not Exec(ExeInstalado, params + ' --resultado "' + archivo + '"', '', SW_HIDE, ewWaitUntilTerminated, rc) then
  begin
    mensaje := 'No se pudo ejecutar el agente: ' + SysErrorMessage(rc);
    exit;
  end;
  if LoadStringsFromFile(archivo, lineas) and (GetArrayLength(lineas) > 0) then
  begin
    Result := (Pos('OK', lineas[0]) > 0) and (Pos('ERROR', lineas[0]) = 0);
    for i := 1 to GetArrayLength(lineas) - 1 do
      mensaje := mensaje + lineas[i] + #13#10;
    mensaje := Trim(mensaje);
  end
  else
    mensaje := 'El agente no dejó un resultado (código ' + IntToStr(rc) + ').';
end;

{ Ventana para volver a escribir el código si el primero no sirvió. }
function PedirCodigoOtraVez(const error: String; var codigo: String): Boolean;
var
  f: TSetupForm;
  texto: TNewStaticText;
  campo: TNewEdit;
  aceptar, cancelar: TNewButton;
begin
  f := CreateCustomForm(ScaleX(400), ScaleY(170), False, False);
  try
    f.Caption := 'Código de vinculación';
    texto := TNewStaticText.Create(f);
    texto.Parent := f;
    texto.Left := ScaleX(16);
    texto.Top := ScaleY(14);
    texto.Width := f.ClientWidth - ScaleX(32);
    texto.Height := ScaleY(60);
    texto.AutoSize := False;
    texto.WordWrap := True;
    texto.Caption := error + #13#10 + 'Escribe el código otra vez (o pide uno nuevo al técnico):';

    campo := TNewEdit.Create(f);
    campo.Parent := f;
    campo.Left := ScaleX(16);
    campo.Top := ScaleY(84);
    campo.Width := f.ClientWidth - ScaleX(32);
    campo.Text := codigo;

    aceptar := TNewButton.Create(f);
    aceptar.Parent := f;
    aceptar.Caption := 'Vincular';
    aceptar.ModalResult := mrOk;
    aceptar.Default := True;
    aceptar.Width := ScaleX(90);
    aceptar.Height := ScaleY(25);
    aceptar.Top := f.ClientHeight - ScaleY(38);
    aceptar.Left := f.ClientWidth - ScaleX(200);

    cancelar := TNewButton.Create(f);
    cancelar.Parent := f;
    cancelar.Caption := 'Cancelar';
    cancelar.ModalResult := mrCancel;
    cancelar.Cancel := True;
    cancelar.Width := ScaleX(90);
    cancelar.Height := ScaleY(25);
    cancelar.Top := aceptar.Top;
    cancelar.Left := f.ClientWidth - ScaleX(104);

    f.ActiveControl := campo;
    Result := f.ShowModal = mrOk;
    if Result then
    begin
      codigo := Uppercase(Trim(campo.Text));
      StringChangeEx(codigo, ' ', '', True);
      StringChangeEx(codigo, '-', '', True);
      Result := codigo <> '';
    end;
  finally
    f.Free;
  end;
end;

procedure CambiarSoporte(permitir: Boolean);
var
  rc: Integer;
  accion: String;
begin
  if permitir then
    accion := 'permitir'
  else
    accion := 'quitar';
  WizardForm.StatusLabel.Caption := 'Cambiando el soporte remoto (puede tardar un par de minutos)...';
  if Exec(ExeInstalado, 'soporte ' + accion, '', SW_HIDE, ewWaitUntilTerminated, rc) and (rc = 0) then
  begin
    if permitir then
      Resumen := Resumen + #13#10 + 'Soporte remoto permitido. Queda apagado hasta que el técnico lo pida.'
    else
      Resumen := Resumen + #13#10 + 'Se quitó el soporte remoto de este equipo.';
  end
  else
    Resumen := Resumen + #13#10 + 'No se pudo cambiar el soporte remoto; vuelve a intentarlo más tarde.';
end;

procedure Vincular;
var
  codigo, sop, msg, otro: String;
begin
  WizardForm.ProgressGauge.Style := npbstMarquee;
  if Actualizando then
  begin
    WizardForm.StatusLabel.Caption := 'Actualizando el agente...';
    Fallo := not Correr('actualizar', msg);
    Resumen := msg;
    if (not Fallo) and (PaginaSoporte.Values[0] <> SoporteActual) then
      CambiarSoporte(PaginaSoporte.Values[0]);
    exit;
  end;

  if PaginaSoporte.Values[0] then
    sop := 'si'
  else
    sop := 'no';
  codigo := PaginaCodigo.Values[0];
  while True do
  begin
    WizardForm.StatusLabel.Caption := 'Vinculando el equipo con tu servicio técnico...';
    if PaginaSoporte.Values[0] then
      WizardForm.FilenameLabel.Caption := 'También se descarga el soporte remoto (RustDesk): puede tardar un par de minutos.';
    if Correr('enroll --codigo ' + codigo + ' --soporte-remoto ' + sop, msg) then
    begin
      Resumen := msg;
      exit;
    end;
    if not PedirCodigoOtraVez(msg, codigo) then
    begin
      Fallo := True;
      Resumen := 'El agente quedó instalado, pero el equipo NO se vinculó:' + #13#10 + msg + #13#10#13#10 +
        'Vuelve a abrir este instalador con un código nuevo.';
      { Si ya tenía otros vínculos, el servicio vuelve a arrancar con ellos. }
      if YaVinculado then
        Correr('actualizar', otro);
      exit;
    end;
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    Vincular;
end;
