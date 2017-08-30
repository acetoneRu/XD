# XD

Standalone I2P BitTorrent Client written in GO

![XD](contrib/logos/xd_logo_256x256.png)

## Features

Current:

* i2p only, no chances of cross network contamination, aka no way to leak IP.
* no java required, works with [i2pd](https://github.com/purplei2p/i2pd)

Soon:

* DHT/Magnet Support

Eventually:

* Maggot Support (?)
* rtorrent compatible RPC (?)

## Dependencies

* GNU Make
* GO 1.9
* node.js 8.x
* yarn 0.24.x


## Building

right now the only support way to build is with `make`

    $ git clone https://github.com/majestrate/XD
    $ cd XD
    $ make

if you do not want to build with embedded webui instead run:

    $ make no-webui

### cross compile for Raspberry PI

Set `GOARCH` and `GOOS` when building with make:

    $ make GOARCH=arm GOOS=linux


## Usage

To autogenerate a new config and start:

    $ ./XD torrents.ini

after started put torrent files into `./storage/downloads/` to start downloading

to seed torrents put data files into `./storage/downloads/` first then add torrent files

To use the RPC Tool symlink `XD` to `XD-CLI`

    $ ln -s XD XD-CLI

to list torrents run:

    $ ./XD-CLI


to add a torrent from http server:

    $ ./XD-CLI add http://somehwere.i2p/some_torrent_that_is_not_fake.torrent
