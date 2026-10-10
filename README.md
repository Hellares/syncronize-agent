# syncronize-agent

Agente de monitoreo para **Windows** que reporta el estado técnico del equipo a Syncronize. El servicio técnico que usa Syncronize lo instala en el equipo del cliente **con su autorización**.

## Qué lee y qué no

| Lee (solo lectura) | NO lee nunca |
|---|---|
| Equipo: marca, modelo, serie, tipo (laptop/escritorio) | Archivos, documentos, fotos |
| Windows: versión, último parche, último reinicio | Historial del navegador |
| CPU, RAM (y módulos), uso | Contraseñas, correos, mensajes |
| Discos: salud, SSD/HDD, temperatura y desgaste | Programas abiertos o ventanas |
| Volúmenes: espacio libre | Capturas de pantalla |
| Batería: salud (capacidad actual / de diseño), ciclos | |
| Red: adaptadores, IP, gateway, DNS | |
| Impresoras: estado y errores (sin papel, atasco…) | |
| Impresoras de red: tinta/tóner, contador de páginas, serie, firmware (IPP y SNMP) | |
| Antivirus: activo, tiempo real, antigüedad de firmas | |

El agente **no ejecuta nada** que le mande el servidor. La única excepción es el [soporte remoto](#soporte-remoto-opcional), que es opcional y el cliente lo autoriza aparte: el servidor solo puede pedir **encender o apagar RustDesk**. Para ver exactamente qué envía, corre `syncronize-agent collect`.

## Instalación en el equipo del cliente

1. En Syncronize: **Servicios → Monitoreo de Equipos → Vincular equipo**. Se elige el cliente, se marca la autorización y sale un código de un solo uso (vence en 24 h).
2. Se lleva `syncronize-agent.exe` al equipo (USB, WhatsApp, descarga) y se le hace **doble clic**.
   - Windows pide permiso de administrador: **Sí**.
   - Se escribe el código y Enter.
3. El agente:
   - vincula el equipo;
   - se copia a `C:\Program Files\Syncronize\Agent\` (el .exe original ya se puede borrar);
   - instala el servicio `SyncronizeAgent` (inicio automático retrasado, se reinicia si falla) y lo arranca.

Después de vincular, el instalador le pregunta **al cliente** si autoriza el [soporte remoto](#soporte-remoto-opcional) para ese servicio técnico. Por defecto, no.

Sin doble clic, en una consola de administrador: `syncronize-agent.exe enroll --codigo ABC123` (`--beta` para el servidor de pruebas; `--soporte-remoto` si el cliente lo autorizó).

Volver a correrlo en un equipo ya instalado muestra con quién está vinculado y ofrece dos opciones:

1. **Actualizar**: detiene el servicio, reemplaza el .exe de Program Files y lo vuelve a arrancar. No pide código.
2. **Agregar otro servicio técnico**: pide el código de esa empresa.
3. **Soporte remoto: autorizar o quitar**, para cada servicio técnico.

### Varios servicios técnicos

Un mismo equipo puede estar vinculado con **varios servicios técnicos a la vez**. Por ejemplo, el cliente lo llevó a un taller y un mes después a otro. Hay un vínculo por empresa y cada uno tiene:

- su token y su consentimiento;
- su modo (a pedido o continuo);
- su cola de reportes (`cola\<agenteId>\`).

Cada técnico ve **solo lo suyo**: no recibe los datos de los otros ni sabe que existen. Los reportes se envían por separado a cada empresa que los pide. Si una empresa desvincula el equipo, el agente quita **solo ese vínculo** y los demás siguen. Volver a vincular con la misma empresa reemplaza su vínculo, no lo duplica. `status` lista todos los vínculos.

Para desinstalar: `"C:\Program Files\Syncronize\Agent\syncronize-agent.exe" uninstall` como administrador. Quita el servicio, la vinculación y la carpeta, y también RustDesk si lo instaló el agente. Si el cliente retira su autorización, se desvincula desde Syncronize y el agente deja de reportar solo.

## Soporte remoto (opcional)

Si el cliente lo autoriza, el técnico puede ver y usar la pantalla del equipo para ayudarlo, con [RustDesk](https://github.com/rustdesk/rustdesk) (código abierto, AGPL-3.0).

- **Autorización aparte, por servicio técnico**. La da el cliente en el instalador, no el técnico. Con varios servicios técnicos, cada uno tiene la suya.
- **El cliente acepta CADA conexión** con un clic, en una ventana de RustDesk (`approve-mode = click`). No hay contraseña que permita entrar sin que lo vea.
- **Apagado por defecto**. El servicio de RustDesk queda en inicio manual y no escucha conexiones. Se enciende solo cuando el técnico toca "Iniciar soporte remoto" en el panel (le llega al agente por la misma pregunta "¿hay pedido?"). Se apaga cuando el técnico lo termina, o a las 2 horas. Si en ese momento hay una sesión abierta, espera a que termine.
- **Instalador oficial sin modificar**. El agente descarga el MSI oficial de una versión fija desde los releases de RustDesk en GitHub y verifica su SHA-256 antes de instalarlo (sin accesos directos ni impresora virtual). La versión y el hash están en `internal/remoto/rustdesk.go`.
- **RustDesk del cliente**. Si el equipo ya tenía RustDesk, el agente lo usa tal cual: no cambia su configuración, no lo apaga si ya estaba encendido y no lo desinstala.
- **Servidor**. Por defecto, el servidor público de RustDesk. El backend puede indicar otro (host y clave) sin reinstalar nada.
- **Quitarlo**. `syncronize-agent soporte quitar` (como administrador), u opción 3 del instalador. Si ningún servicio técnico queda autorizado, o se desinstala el agente, se desinstala RustDesk si lo puso el agente.

Cada vez que un técnico enciende el soporte remoto queda registrado en Syncronize quién lo encendió y cuándo.

## Comandos

| Comando | Qué hace |
|---|---|
| `collect` | Imprime el reporte en JSON. No envía nada. |
| `status` | Muestra la vinculación, el servicio y los reportes en cola. |
| `send` | Toma un reporte y lo envía ya (prueba de conexión). |
| `start` / `stop` / `restart` | Controlan el servicio. |
| `run` | Corre en primer plano, para diagnóstico. |
| `uninstall` | Quita el servicio y borra la vinculación (y RustDesk si lo instaló el agente). |
| `soporte` | Muestra la autorización del soporte remoto por servicio técnico y el estado de RustDesk (encendido, ID, sesión abierta). |
| `soporte permitir` / `soporte quitar` | Autoriza o quita el soporte remoto para todos los servicios técnicos. |

## Cómo funciona

- **A pedido (por defecto)**: el agente NO manda reportes por su cuenta. Mantiene una pregunta abierta al backend ("¿hay pedido?", `GET /agentes/pedido`) que el backend retiene ~25 s. Cuando el técnico toca **Actualizar datos** en el panel, el backend contesta en el acto y el agente toma un reporte y lo envía (llega en segundos). Esa pregunta es liviana (sin cuerpo) y además le dice al panel si el equipo está **en línea**.
- **Continuo**: si en el panel se activa "Monitoreo continuo", además reporta cada 15 minutos (el backend puede cambiar el intervalo), con alertas al celular y aviso de "sin reportar". El modo lo manda el backend en cada respuesta: se cambia sin tocar el equipo.
- **Servidor viejo** (sin `/agentes/pedido`, 404): reporta cada intervalo, como v0.2.
- **Arranque escalonado** (continuo): espera entre 5 y 30 s al arrancar, para que muchas PCs no golpeen juntas.
- **Sin internet**: los reportes esperan en una cola en disco (`cola\`). Caben hasta 50, unas 12 h; se envían del más viejo al más nuevo.
- **Datos**: viven en `%ProgramData%\Syncronize\Agent`. Solo SYSTEM y Administradores tienen permisos ahí, porque el token no lo debe leer un usuario común.
  - `config.json`: la lista `vinculos`, cada uno con servidor, agenteId, token, empresa, fecha, intervalo y modo (`continuo`). Lee también el formato de v0.2-v0.4, que tenía un solo vínculo en la raíz.
  - `cola\<agenteId>\*.json`: reportes pendientes de cada vínculo.
  - `agent.log`: rota al pasar 1 MB.
- **Errores de lectura**: si una sección falla (WMI roto, sin permisos), el resto del reporte sigue y el error va en `errores[]`.
- **Impresoras de red**: además de lo que dice Windows, el agente le pregunta a la impresora misma, solo lectura. Por **IPP** (puerto 631, `https` y si no `http`) obtiene las tintas o el tóner (`marker-*`), las alertas (`printer-state-reasons`: atasco, sin papel, almohadillas llenas…), el modelo y el firmware. Por **SNMP v2c** (comunidad `public`, Printer MIB) obtiene el contador total de páginas y la serie. La IP sale del puerto TCP/IP, del registro de WSD (`Enum\SWD\DAFWSDProvider`) o del nombre del puerto. Tiene un tope de 12 s para todas. Si la misma impresora está instalada por USB y por WiFi (mismo modelo, una sola de red), la de USB copia los datos y lo indica con `datosDe`. Las que están solo por USB o Bluetooth no tienen un estándar: quedan con lo de Windows.
- **Datos que piden administrador**: la temperatura, el desgaste y las horas del disco solo se leen como administrador. El servicio corre como SYSTEM; `collect` en una consola normal los muestra en `null`.

## Contrato con el backend

```
POST {servidor}/agentes/vincular   {codigo, reporte}            → {agenteId, token, intervaloSegundos}
POST {servidor}/agentes/reporte    Authorization: Bearer token  → {intervaloSegundos?, continuo?}
GET  {servidor}/agentes/pedido     Authorization: Bearer token  → {reportar, continuo, intervaloSegundos, soporteRemoto?}
POST {servidor}/agentes/soporte-remoto  Authorization: Bearer token, {permitido, estado?, id?, detalle?, version?}
```

- `soporteRemoto` en `/pedido`: `{restanteSegundos, host, key}`. `restanteSegundos` es `null` si está apagado y `0` si se venció el plazo. Van segundos y no una hora porque el reloj del equipo puede estar mal. `host`/`key` vacíos = servidor público de RustDesk. Solo cuenta si el cliente autorizó a esa empresa.
- `/soporte-remoto`: el agente lo manda al arrancar y cada vez que cambia el estado (`INACTIVO`, `INSTALANDO`, `ACTIVO` con el `id` de RustDesk, `ERROR` con `detalle`). Con `permitido: false`, el backend apaga y olvida el ID.

- Los cuerpos van en JSON comprimido con gzip (`Content-Encoding: gzip`).
- La forma del reporte está en `internal/collector/types.go`. `version` sube solo si cambia la forma, no por campos opcionales nuevos.
- Respuestas de `/reporte` que el agente interpreta:

| Respuesta | Qué hace el agente |
|---|---|
| 401 / 403 | Toma el token como revocado y deja de reportar. |
| 400 / 413 / 422 | Descarta ese reporte. |
| Otro error | Reintenta en el próximo ciclo. |

## Desarrollo

Requiere Go 1.24 o superior.

```
.\build.ps1 -Version 0.2.0         # dist\syncronize-agent.exe (producción)
.\build.ps1 -Version 0.2.0 -Beta   # dist\syncronize-agent-beta.exe (doble clic vincula contra beta)
go test ./...
```

`syncronize-agent.exe version` dice a qué servidor apunta el doble clic.

Cada release se compila en GitHub Actions (`.github/workflows/build.yml`) desde el código de este repositorio, con `build.ps1`.

## Privacidad

El agente envía datos **solo al servidor de Syncronize** (`saas.syncronize.net.pe`) y **solo a los servicios técnicos con los que el dueño del equipo lo vinculó**, con su autorización.

- **Qué envía**: únicamente el estado técnico listado en [Qué lee y qué no](#qué-lee-y-qué-no) (equipo, Windows, CPU, memoria, discos, batería, red, impresoras, antivirus). Nunca archivos, documentos, fotos, contraseñas, historial ni capturas de pantalla.
- **Cuándo**: a pedido, cuando el técnico toca "Actualizar datos". Solo si el técnico activa el monitoreo continuo, también cada 15 minutos.
- **Soporte remoto**: solo con una autorización aparte del cliente, y el cliente acepta cada conexión en su pantalla. El agente informa únicamente si RustDesk está encendido y su ID. Ver [Soporte remoto](#soporte-remoto-opcional).
- **Aviso al instalar**: el instalador explica qué lee y pide el código que el técnico genera tras registrar la autorización del cliente.
- **Cómo desactivarlo**: el técnico lo desvincula desde Syncronize, o se desinstala en el equipo con `"C:\Program Files\Syncronize\Agent\syncronize-agent.exe" uninstall`. Esto quita el servicio, los vínculos y la carpeta del programa.
- Política de privacidad de Syncronize: https://syncronize.net.pe/privacidad

*Privacy statement (English)*: this program only transfers the technical information listed above to the Syncronize server, and only for the service providers the device owner explicitly linked it to. It never reads personal files, passwords or browsing history. It can be unlinked from Syncronize or uninstalled at any time (`syncronize-agent.exe uninstall`).

## Code signing policy

Free code signing provided by [SignPath.io](https://about.signpath.io), certificate by [SignPath Foundation](https://signpath.org).

- **Committers and reviewers**: [@Hellares](https://github.com/Hellares)
- **Approvers**: [@Hellares](https://github.com/Hellares)

Every signed binary is built by GitHub Actions from the source code in this repository. Each release is reviewed and approved manually before it is signed.

## Licencia

[MIT](LICENSE) © 2026 Syncronize SAC. El agente es libre y gratuito; el servicio de Syncronize (panel y servidor) es un producto aparte.

RustDesk no forma parte de este repositorio ni de sus binarios: el agente descarga e instala el instalador oficial, sin modificar, que se distribuye con su propia licencia ([AGPL-3.0](https://github.com/rustdesk/rustdesk/blob/master/LICENCE)).
