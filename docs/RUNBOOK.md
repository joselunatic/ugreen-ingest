# Moy Media Ingest - Runbook de desarrollo y despliegue

**Aplicación web de ingesta SD / CFexpress integrada con UGREEN NASync DXP4800 Pro**  
v1.0 - 7 agosto 2026

> Este proyecto pertenece a Moy Santana, productor audiovisual. No corresponde a Damal Redes ni debe tratarse como infraestructura del usuario que ayuda a desarrollarlo. El objetivo es una solución simple, práctica y mantenible para dos personas de confianza; evitar sobreingeniería y capas de seguridad innecesarias.

# 1. Resumen ejecutivo

Construir una appliance de ingesta audiovisual que se ejecute en un UGREEN NASync DXP4800 Pro con UGOS Pro. Lectores USB de SD/CFexpress se conectarán directamente al NAS. Al insertar una tarjeta, una web app accesible desde una tablet pedirá el número/cámara de origen, copiará el contenido al almacenamiento del NAS, verificará la copia, mantendrá historial y mostrará de forma inequívoca cuándo es seguro expulsar o, opcionalmente, formatear la tarjeta.

La primera versión se desarrollará en Windows 11 + WSL2, inicialmente con tarjetas simuladas mediante directorios locales. Después se probará hot-plug USB real en WSL2 usando usbipd-win y finalmente se adaptará/validará contra Docker y los dispositivos USB reales de UGOS Pro.

# 2. Principios del proyecto

- **Simplicidad primero.** Dos personas de confianza van a trabajar en el proyecto. No introducir IAM complejo, clusters, GitLab/Gitea local, colas distribuidas, Kubernetes, microservicios innecesarios ni CI/CD sofisticado.
- **GitHub es la fuente de verdad.** Repositorio privado. Preferentemente propiedad de Moy, con el usuario como colaborador.
- **El NAS procesa los datos.** La tablet sólo es un cliente web/HMI; no copia ni verifica archivos.
- **Nada de borrado automático.** La tarjeta nunca se formatea ni borra sólo porque una copia haya terminado.
- **Verificar antes de declarar seguro.** “Copia terminada” y “copia verificada” son estados distintos.
- **Desarrollar sin hardware real.** La aplicación debe disponer desde el inicio de un modo mock que permita avanzar sin NAS ni lectores.
- **Diseñar para Docker/Linux.** El destino final es UGOS Pro sobre un NAS x86, no Windows.
- **Añadir funciones cuando hagan falta.** rclone/cloud, MHL, múltiples destinos y automatizaciones avanzadas son extensiones, no requisitos del MVP.

# 3. Hardware y contexto de destino

## 3.1 NAS

- Modelo objetivo: **UGREEN NASync DXP4800 Pro**.
- CPU: Intel Core i3-1315U; arquitectura x86-64.
- 4 bahías SATA y 2 ranuras M.2 NVMe.
- Red: 10GbE + 2.5GbE.
- Puertos USB rápidos de 10 Gb/s disponibles para lectores; evitar usar USB 2.0 para ingesta rápida.
- UGOS Pro ofrece aplicación Docker/Container Manager y soporte de Docker Compose. SSH se usará para desarrollo/despliegue cuando el NAS esté disponible.
- La POC debe validar qué nivel de acceso a /dev, mount/umount y hot-plug permite exactamente el Docker de UGOS Pro.

## 3.2 Discos iniciales

- Moy ya ha comprado **2 x Seagate IronWolf Pro ST24000NT002 de 24 TB**.
- Inicio recomendado: **RAID 1**, aproximadamente 24 TB nominales utilizables antes de overhead/formato.
- Las bahías 3 y 4 quedarán libres inicialmente.
- No comprar NVMe para el MVP antes de medir el rendimiento real. Más adelante se podrá añadir un RAID1 NVMe como volumen ACTIVE/INGEST si la edición o la ingesta compiten con los HDD.
- La posible evolución a 4 discos se decidirá después (RAID5 o dos RAID1); no debe condicionar el primer desarrollo de la app.

## 3.3 Red y estaciones de edición

- El NAS usará 10GbE hacia un switch multigig/10GbE.
- Hasta 3 MacBook pueden editar desde el NAS.
- Para comenzar, 2.5GbE por Mac es razonable (~250-290 MB/s reales típicos), dejando el uplink del NAS a 10GbE. Si un editor necesita más, podrá pasar a Thunderbolt 10GbE.
- Separación práctica recomendada: 10GbE para datos/edición y 2.5GbE para gestión/servicios auxiliares si resulta útil; no es necesario bonding.

## 3.4 Lectores y HMI

- Objetivo: lectores USB 3.2 Gen2 conectados directamente al NAS. Preferencia inicial: lectores duales profesionales CFexpress Type B + SD UHS-II, por ejemplo ProGrade Digital PG05.5; OWC/Lexar son alternativas.
- Evitar un hub compartido al principio si el NAS permite conectar dos lectores a puertos 10Gb/s independientes.
- HMI: tablet Android económica de 8-10 pulgadas en horizontal, por ejemplo Samsung Galaxy Tab A9 8.7, ejecutando un navegador en modo kiosco.
- La tablet accede por Wi-Fi/LAN a la web local del NAS. No necesita almacenamiento, potencia ni Ethernet de alta velocidad.

# 4. Experiencia de usuario objetivo

La operación debe poder realizarse sin abrir UGOS, Docker ni un terminal. Un operador inserta la tarjeta, toca la cámara correspondiente y espera hasta recibir una indicación inequívoca de seguridad.

## 4.1 Flujo normal

1. Se inserta una SD o CFexpress en un lector conectado al NAS.
2. El backend detecta un nuevo medio y determina lector, dispositivo, filesystem, tamaño y UUID/identificador disponible.
3. La UI cambia automáticamente a **NUEVA TARJETA DETECTADA**.
4. El usuario selecciona **CAM 1**, **CAM 2**, **CAM 3**, u otra categoría configurable (DRONE, AUDIO, etc.).
5. La aplicación asigna el siguiente número de tarjeta para esa cámara dentro del proyecto/evento actual.
6. Se inicia la copia hacia la ruta de destino.
7. La UI muestra bytes copiados, total, porcentaje, MB/s, tiempo y estado.
8. Finalizada la escritura, comienza una fase independiente de verificación.
9. Si la verificación pasa, el estado cambia a **VERIFIED / SAFE TO EJECT**.
10. El usuario puede pulsar **EXPULSAR**. Opcionalmente puede pulsar **FORMATEAR**, siempre con confirmación explícita adicional.

## 4.2 Estados mínimos obligatorios

```text
DETECTED
  -> WAITING_CAMERA
  -> COPYING
  -> VERIFYING
  -> VERIFIED
  -> SAFE_TO_EJECT
  -> EJECTED

ERROR puede producirse desde COPYING o VERIFYING.
FORMAT_AVAILABLE sólo debe existir después de VERIFIED.
```

- Mientras COPY/VERIFY esté activo: mostrar **NO RETIRAR TARJETA**.
- No utilizar verde/“seguro” al terminar únicamente la copia.
- Si la verificación falla: conservar la tarjeta montada, registrar el fallo y mostrar instrucciones de reintento; no ofrecer formateo.
- El formateo no forma parte del camino crítico del MVP; puede implementarse después del flujo seguro de copia/verificación/expulsión.

# 5. Organización de archivos

La estructura debe ser simple y legible sin necesitar la aplicación. Ejemplo de destino:

```text
/INGEST/
  /2026-08-07_NOMBRE_EVENTO/
    /CAM01/
      /CARD001/
      /CARD002/
    /CAM02/
      /CARD001/
    /CAM03/
    /DRONE/
    /AUDIO/
```

- No renombrar archivos originales dentro de la tarjeta en el MVP.
- Preservar jerarquía relativa y timestamps cuando sea razonable.
- Evitar colisiones: un CARDxxx ya ingerido no debe sobrescribirse silenciosamente.
- El proyecto/evento actual debe poder crearse o seleccionarse desde la UI o configuración.
- El identificador de cámara y contador de tarjeta se guardarán también en SQLite.

# 6. Persistencia e historial

Usar SQLite. Es suficiente para este sistema de un solo NAS y simplifica backup, despliegue y diagnóstico.

Modelo mínimo sugerido para una ingesta:

```text
id
project_id / project_name
camera_id
card_number
reader_id
source_device
source_uuid
source_label
source_size_bytes
destination_path
started_at
copy_completed_at
verify_completed_at
finished_at
file_count
bytes_total
bytes_copied
copy_status
verify_status
error_message
ejected_at
formatted_at (nullable)
```

- Guardar suficiente información para evitar duplicados accidentales y poder auditar qué tarjeta se copió dónde.
- No almacenar secretos ni datos innecesarios.
- La base de datos debe residir en un volumen persistente fuera de la imagen Docker.

# 7. Arquitectura de software recomendada

Partir conceptualmente de `cardingest` (Go, appliance Linux, UI web, SQLite, detección de lectores y hot-plug) y reutilizar ideas de `sd-card-media-ingest` para estados seguros y confirmación humana. Wrangler sirve como referencia de UX pero no como base principal porque su detección real está más vinculada a macOS/Windows.

```text
UGREEN / Docker

  moy-ingest (preferible: un único servicio al inicio)
  +-- device detector / source provider
  +-- ingest controller
  +-- copy engine
  +-- verification engine
  +-- SQLite
  +-- REST API
  +-- SSE event stream
  +-- embedded Web UI

Tablet ---> HTTP ---> moy-ingest
Readers ---> USB ---> Linux/UGOS ---> moy-ingest
Storage <--- filesystem <--- moy-ingest
```

- Preferir **un monolito pequeño** antes que varios microservicios.
- Go es una buena elección si se forkea `cardingest`: binario único, fácil de contenerizar y adecuado para acceso Linux a dispositivos.
- REST para operaciones de usuario/configuración; SSE para progreso/estados en tiempo real. WebSocket no es obligatorio.
- Frontend sencillo y touch-first, servido por el mismo backend si es posible.
- El copy engine debe ser abstraíble para que el modo mock y el modo real utilicen la misma máquina de estados.

# 8. Proyectos OSS de referencia

- `michaelpeterswa/cardingest`: candidato principal para fork/base. Appliance de ingesta, Go, UI web, SQLite, Linux real y Compose de appliance.
- `innate-inc/sd-card-media-ingest`: referencia de arquitectura, separación del display, confirmación antes de borrado y uso de rclone en tareas auxiliares.
- `Joshuaeal/wrangler`: referencia de UX/funciones de ingesta verificadas y múltiples destinos, pero no primera base para Linux/UGOS.
- `ascmitc/mhl`: referencia futura para ASC Media Hash List. No integrar MHL en el MVP salvo que resulte trivial.
- Herramientas como rclone son válidas para backup/replicación posterior; **no son requisito para la primera copia local tarjeta -> NAS**.

Repositorios de referencia: https://github.com/michaelpeterswa/cardingest ; https://github.com/innate-inc/sd-card-media-ingest ; https://github.com/Joshuaeal/wrangler ; https://github.com/ascmitc/mhl

# 9. Acceso a USB en Docker: diseño y estrategia

## 9.1 Estrategia A - preferida si UGOS la facilita

Dejar que UGOS/Linux monte el dispositivo externo y entregar al contenedor únicamente el filesystem de la tarjeta en read-only. Esta opción reduce permisos y hace más simple el contenedor.

```text
Reader -> UGOS automount -> /ruta/external/card -> bind mount :ro -> container
                                            
NAS destination --------------------------------> bind mount :rw -> container
```

## 9.2 Estrategia B - appliance con control de dispositivo

Si el automount de UGOS no ofrece rutas/identificadores estables, usar el modelo de `cardingest`: exponer `/dev` al contenedor y permitir mount/umount. El Compose de appliance del proyecto usa SYS_ADMIN y desactiva AppArmor para permitir hot-plug y mount.

```yaml
services:
  ingest:
    volumes:
      - /dev:/dev
      - /path/to/ingest:/data/ingest
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
```

Esto **debe validarse físicamente en UGOS Pro**. Docker/Compose están soportados por UGREEN, pero la documentación pública no garantiza específicamente este patrón de block-device passthrough. No inventar una solución antes de probarlo.

## 9.3 POC obligatoria en el NAS

```bash
lsusb
lsblk
ls -l /dev/disk/by-path/ | grep usb

# después repetir las comprobaciones dentro de un contenedor de prueba
```

Criterio de éxito: el lector y la tarjeta aparecen como block device Linux, el contenedor puede observar hot-plug y el sistema puede montar la tarjeta read-only y desmontarla de forma controlada.

# 10. Copy y verificación

- En el MVP, copiar directamente usando Go/filesystem o reutilizar el motor de `cardingest`; no introducir rclone en el camino crítico salvo necesidad concreta.
- La verificación debe releer el destino y compararlo con el origen mediante hash o la estrategia de verificación elegida. El estado VERIFIED sólo puede alcanzarse si la comprobación finaliza correctamente.
- Algoritmo recomendado para la primera versión: xxHash para velocidad o SHA-256 si se prioriza simplicidad/ubicuidad. El diseño debe permitir cambiarlo.
- ASC-MHL es una extensión deseable para workflow audiovisual profesional, pero después de estabilizar el núcleo.
- El destino debe tratarse como escritura normal del filesystem del NAS; no asumir que RAID sustituye la verificación de ingesta.

# 11. UI de tablet

Diseñar primero para una pantalla 8.7 pulgadas en horizontal. Debe funcionar igualmente en un navegador de escritorio para desarrollo y diagnóstico.

## 11.1 Pantalla idle

```text
MEDIA INGEST

Esperando tarjeta...

NAS: OK    Storage: OK
```

## 11.2 Tarjeta detectada

```text
CFEXPRESS DETECTADA
476 GB

Selecciona cámara

[ CAM 1 ]  [ CAM 2 ]  [ CAM 3 ]
[ DRONE ]  [ AUDIO ]  [ OTRA ]
```

## 11.3 Copia/verificación

```text
CAM 2 - CARD 018

████████████████---- 82%
391 / 476 GB     487 MB/s

COPY      EN CURSO
VERIFY    PENDIENTE

NO RETIRAR TARJETA
```

## 11.4 Final

```text
CAM 2 - CARD 018

COPY       OK
VERIFY     OK

SEGURO RETIRAR

[ EXPULSAR ]
[ FORMATEAR ]
```

- Botones grandes y pocos elementos.
- Estado legible a distancia. Color puede reforzar estado, nunca ser el único indicador.
- Si se implementa FORMATEAR: pantalla/diálogo adicional de confirmación; jamás acción de un toque.
- No construir app Android nativa. La tablet usa navegador/kiosk contra la web local.

# 12. Entorno inicial: Windows 11 + WSL2

El desarrollo empieza en el equipo Windows 11 del colaborador, dentro de una distro Linux WSL2. Mantener el repositorio en el filesystem Linux (`~/src/...`), no bajo `/mnt/c`, para una experiencia Docker/FS más cercana a Linux nativo.

## 12.1 Setup recomendado

```bash
# PowerShell (administrador, si WSL no está instalado)
wsl --install -d Ubuntu-24.04
wsl --update

# Dentro de Ubuntu
sudo apt update
sudo apt install -y git make golang-go sqlite3 rclone usbutils udev jq curl
mkdir -p ~/src
cd ~/src
```

Docker: usar Docker Desktop con backend WSL2 e integración con Ubuntu, o Docker Engine directamente dentro de WSL2 si ya se prefiere ese flujo. Para este proyecto importa que los contenedores sean Linux y que Docker Compose funcione; no dedicar tiempo a optimizar la elección mientras funcione correctamente.

## 12.2 Modos de ejecución

```text
INGEST_MODE=mock
# Fuentes simuladas: ./dev/cards/CARD_A, ./dev/cards/CARD_B...

INGEST_MODE=real
# Fuentes descubiertas desde dispositivos/mounts Linux reales.
```

El modo mock es un requisito de diseño, no un hack temporal. Debe permitir a Codex/Claude ejecutar tests y desarrollar UI/lógica sin lectores.

# 13. USB real en WSL2

Windows 11 puede adjuntar dispositivos USB a WSL2 mediante usbipd-win. Esto permite probar lectores reales antes de acceder al NAS. Mientras un dispositivo está adjunto a WSL, Windows deja de usarlo.

```bash
# PowerShell administrador: instalar usbipd-win
winget install --interactive --exact dorssel.usbipd-win

# enumerar
usbipd list

# compartir una vez (admin)
usbipd bind --busid <BUSID>

# adjuntar a WSL
usbipd attach --wsl --busid <BUSID>

# dentro de WSL
lsusb
lsblk
ls -l /dev/disk/by-path/ | grep usb

# desconectar
usbipd detach --busid <BUSID>
```

No bloquear el desarrollo si un lector específico no se expone exactamente igual que en UGOS. La integración definitiva de block devices sólo se acepta después de la POC física en el DXP4800 Pro.

# 14. Estructura de repositorio sugerida

```text
moy-media-ingest/
├── cmd/
│   └── ingest/
├── internal/
│   ├── device/
│   ├── ingest/
│   ├── copy/
│   ├── verify/
│   ├── storage/
│   ├── database/
│   └── api/
├── web/
│   ├── static/
│   └── templates/        # si se usa server-rendered/embedded UI
├── migrations/
├── config/
│   └── config.example.yaml
├── dev/
│   └── cards/            # fixtures mock, no media pesada en Git
├── scripts/
├── tests/
├── Dockerfile
├── compose.yml
├── compose.dev.yml
├── .env.example
├── .gitignore
├── README.md
└── RUNBOOK.md
```

Si al forkear `cardingest` su estructura existente es razonable, **no reorganizar todo sólo para parecerse a este árbol**. Mantener cambios pequeños y comprensibles.

# 15. Configuración

```yaml
mode: mock                         # mock | real
listen: 0.0.0.0:8080
database: /data/state/ingest.db
destination_root: /data/ingest
mock_root: /data/mock-cards

project:
  default_name: TEST_EVENT

cameras:
  - CAM01
  - CAM02
  - CAM03
  - DRONE
  - AUDIO

verification:
  algorithm: xxhash

formatting:
  enabled: false
```

- No hardcodear `/volume1` o rutas específicas de UGOS dentro de la lógica; mapearlas a `/data/...` en Docker.
- El `.env`/config real no entra en Git. Sí versionar `.env.example` y `config.example.yaml`.
- El nombre del proyecto/evento y cámaras deben ser configurables sin recompilar.

# 16. API mínima sugerida

```text
GET  /health
GET  /api/v1/status
GET  /api/v1/readers
GET  /api/v1/cards
GET  /api/v1/ingests
GET  /api/v1/ingests/:id
POST /api/v1/ingests/:id/assign-camera
POST /api/v1/ingests/:id/eject
POST /api/v1/ingests/:id/format        # inicialmente deshabilitado
GET  /api/v1/events                    # SSE
GET  /                                 # tablet UI
```

No convertir esta lista en obligación si `cardingest` ya tiene endpoints equivalentes. Reutilizar lo existente y adaptar sólo lo necesario.

# 17. Etapas de desarrollo

## Fase 0 - Preparación

Repositorio privado, fork/revisión de cardingest, toolchain WSL2, Docker Compose funcionando, RUNBOOK.md incluido.

## Fase 1 - MVP mock

UI touch-first, proyectos/cámaras, máquina de estados, SQLite, copy+verify entre directorios locales, SSE, historial y healthcheck. Cero USB real.

## Fase 2 - Robustez local

Reanudación/error handling básico, prevención de sobrescritura, tarjetas duplicadas, cancelación segura si procede, tests unitarios/integración.

## Fase 3 - USB real en WSL2

usbipd-win, identificación de lectores, block devices, mount read-only, hotplug, eject/umount. Probar con SD/USB de prueba; no usar material único.

## Fase 4 - Docker appliance

Contenedor Linux con volúmenes persistentes. Validar modo mock dentro de Docker y luego device passthrough local si es viable.

## Fase 5 - POC UGOS

SSH al NAS, comprobar lsusb/lsblk/by-path, probar automount UGOS, probar Compose con /dev/SYS_ADMIN sólo si hace falta. Elegir Estrategia A o B.

## Fase 6 - Datos reales

Configurar RAID1, shares/rutas, conectar lectores reales y medir copia/verificación. Probar tarjetas de muestra antes de producción.

## Fase 7 - Tablet

Configurar tablet en modo kiosco, probar reconexión, actualización SSE, estados y experiencia de operador.

## Fase 8 - CI/CD sencillo

Push a GitHub; GitHub Action opcional para SSH al NAS y `git pull && docker compose up -d --build`. No añadir GHCR/rollback complejo hasta necesitarlo.

## Fase 9 - Extensiones

Formateo controlado, MHL, rclone/cloud, múltiples destinos, NVMe active ingest, notificaciones y métricas sólo si aportan valor real.

# 18. Tests y criterios de aceptación del MVP

## 18.1 Funcionales

- Detectar una fuente mock nueva y reflejarla en la UI sin refresco manual.
- Asignar cámara y generar CARDxxx correctamente.
- Copiar un árbol con archivos/subdirectorios sin alterar el origen.
- Mostrar progreso coherente y estado NO RETIRAR durante copia/verificación.
- Verificar el destino y alcanzar VERIFIED sólo después de éxito.
- Detectar una corrupción introducida deliberadamente y terminar en ERROR, nunca SAFE_TO_EJECT.
- No sobrescribir un CARDxxx existente.
- Persistir historial tras reiniciar el proceso/contenedor.
- Endpoint `/health` responde correctamente.
- UI usable con ratón y pantalla táctil, a 8.7 pulgadas horizontal.

## 18.2 USB/NAS posteriores

- Hot-plug real detectado.
- Tarjeta montada read-only para la fase de copia.
- Desconexión física durante COPY produce error controlado y no marca la ingesta como verificada.
- EJECT desmonta antes de indicar que puede retirarse.
- Dos lectores simultáneos se distinguen correctamente.
- Reinicio del contenedor no formatea ni elimina datos por sí mismo.
- Velocidad y uso de CPU/disco se registran durante una ingesta real para decidir posteriormente si hacen falta NVMe.

# 19. CI/CD y colaboración

- GitHub privado. Moy como propietario si es posible; colaborador con acceso de escritura para quien desarrolla.
- Ramas simples: `main` estable; `feature/...` para cambios. Una rama `dev` sólo si realmente ayuda.
- Codex y Claude Code pueden trabajar sobre el mismo repo; commits pequeños y descriptivos.
- Cuando el NAS esté disponible: Tailscale + SSH con clave para acceder remotamente. No hace falta montar Git server local en el NAS.
- Despliegue inicial deliberadamente simple: Action o comando manual por SSH -> `git pull --ff-only` -> `docker compose up -d --build`.
- No exigir approvals, runners autoalojados, firma compleja, GHCR ni despliegues blue/green en la primera versión.

## 19.1 Ejemplo de deploy simple futuro

```bash
ssh moy-nas <<'EOF'
  cd /volume1/dev/moy-media-ingest
  git pull --ff-only
  docker compose up -d --build
  curl -fsS http://127.0.0.1:8080/health
EOF
```

# 20. Despliegue previsto en UGOS

```text
DXP4800 Pro
├── HDD RAID1 (2 x 24 TB)
│   └── /INGEST o share equivalente
├── /appdata/moy-ingest
│   ├── ingest.db
│   └── config.yaml
├── Docker
│   └── moy-ingest :8080
├── USB reader 1
├── USB reader 2 (opcional al inicio)
└── LAN/Wi-Fi -> tablet
```

Las rutas reales de UGOS se determinarán durante la POC; dentro del contenedor mantener rutas estables como `/data/ingest` y `/data/state`.

# 21. POC UGOS - checklist pormenorizado

1. Actualizar UGOS Pro y comprobar funcionamiento general del NAS.
2. Instalar/activar Docker y habilitar SSH.
3. Crear el RAID1 con los dos IronWolf Pro de 24 TB y un destino temporal de pruebas.
4. Conectar un único lector y una tarjeta desechable/de prueba.
5. Por SSH ejecutar `lsusb`, `lsblk`, `blkid`, `mount` y revisar `/dev/disk/by-path/`.
6. Observar si UGOS automonta la tarjeta y anotar la ruta exacta y su estabilidad al reconectar.
7. Probar Estrategia A: bind del mount de UGOS como read-only en un contenedor trivial.
8. Si A no es suficiente, probar Estrategia B con `/dev:/dev`, `SYS_ADMIN` y mount manual dentro de un contenedor de prueba.
9. Comprobar hot-plug después de arrancar el contenedor.
10. Validar umount/eject.
11. Sólo después desplegar `moy-ingest` en modo real.
12. Copiar datos de prueba y provocar deliberadamente un hash incorrecto para validar el estado ERROR.
13. Probar dos lectores si el hardware previsto los requiere.
14. Medir MB/s y carga. No optimizar antes de medir.

# 22. Qué NO debe hacer Claude/Codex inicialmente

- No implementar Kubernetes, Redis, RabbitMQ, PostgreSQL ni microservicios.
- No construir una app Android/iOS nativa.
- No montar un servidor Git en el NAS.
- No crear un sistema multiusuario complejo; una UI local sin login es aceptable inicialmente si sólo vive en la LAN/Tailscale.
- No automatizar el formateo tras una ingesta.
- No añadir cloud/rclone/MHL al camino crítico del MVP.
- No asumir rutas `/volume1` ni detalles internos de UGOS hasta probar el NAS.
- No asumir que los dispositivos USB serán `/dev/sda`, `/dev/sdb`; usar by-path/UUID/metadata estable.
- No guardar material audiovisual de prueba pesado en Git.
- No optimizar para 10GbE/CFexpress máximo antes de que el flujo sea correcto y verificable.

# 23. Primer backlog para el agente

1. Leer este RUNBOOK completo y los repos `cardingest` y `sd-card-media-ingest` antes de decidir si se forkea o se inicia un repo limpio.
2. Proponer una decisión de base: fork mínimo de `cardingest` o implementación limpia en Go reutilizando ideas; justificar por mantenimiento y cantidad de cambios necesarios.
3. Preparar el proyecto para Windows 11/WSL2 y documentar comandos de arranque.
4. Implementar `INGEST_MODE=mock` con un provider de directorios locales.
5. Implementar SQLite y la máquina de estados.
6. Implementar proyecto/cámara/card-number.
7. Implementar copy + verify con progreso.
8. Implementar API + SSE.
9. Crear la UI táctil responsive.
10. Añadir tests automatizados para success, hash mismatch, source removal y destination collision.
11. Añadir Dockerfile + Compose para modo mock.
12. Documentar qué partes quedan pendientes específicamente de hardware: usbipd/WSL y UGOS.
13. No implementar format hasta que las fases anteriores estén estables.

# 24. Definition of Done para “desarrollo inicial”

Antes de conectar el NAS de Moy, la versión inicial se considera satisfactoria si desde WSL2 puede arrancarse con un único comando y demostrar el flujo completo usando directorios simulados:

```bash
docker compose -f compose.yml -f compose.dev.yml up --build
```

y desde `http://localhost:8080` se puede crear/seleccionar un evento, simular la aparición de una tarjeta, seleccionar cámara, verla copiarse, verificar correctamente, ver historial y recibir SAFE_TO_EJECT; una modificación deliberada del destino debe producir ERROR. Todo debe persistir tras reiniciar el contenedor.

# 25. Referencias técnicas

- UGREEN DXP4800 Pro / UGOS Pro y Docker: documentación oficial de UGREEN NAS.
- Docker Desktop WSL2: https://docs.docker.com/desktop/features/wsl/
- Microsoft WSL USB/usbipd-win: https://learn.microsoft.com/windows/wsl/connect-usb
- cardingest: https://github.com/michaelpeterswa/cardingest
- sd-card-media-ingest: https://github.com/innate-inc/sd-card-media-ingest
- Wrangler: https://github.com/Joshuaeal/wrangler
- ASC MHL: https://github.com/ascmitc/mhl

# 26. Instrucción de arranque para Claude Code / Codex

Usa este documento como fuente de requisitos. Empieza por inspeccionar los repositorios OSS indicados y el entorno local disponible. Prioriza una primera implementación funcional en modo mock dentro de WSL2. Mantén el diseño simple, Linux/Docker-first y preparado para sustituir el provider mock por detección real de dispositivos. Antes de introducir una dependencia o servicio adicional, demuestra qué problema concreto del MVP resuelve. No desarrolles funciones que dependan de UGOS sin aislarlas detrás de una interfaz y marcarlas explícitamente como pendientes de validación física.

