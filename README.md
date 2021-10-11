yt-nncp
=======

Queue up Youtube downloads through youtube-dl over NNCP.

## Usage
```
yt-nncp -pipe <fifo>
```

`yt-nncp` will open the named pipe `<fifo>` for reading from. Each
line that is read from the named pipe is parsed as the following:

```
<dest node> <media url> <<quality>>
```

where `<quality>` is optional and can be either `best`, `worst`, or
`medium`. An optional parameter `-max` can be passed which controls
the maximum number of simultaneous downloads ongoing. By default this
is set to `3` but can be changed.

Make sure to set the environment varables if needed:

1. `NNCP_PATH` for the path to `nncp-file` if it is not already in your path.
2. `NNCP_CFG_PATH` for the path to your NNCP node config if it's not at the default path.

Further flags are available by running `yt-nncp -h`

## Using with NNCP
This bot requires a named pipe to exist which it can read from to
queue up media downloads. `fifo-recv.sh` is a simple script which
first appends the value of the envar `NNCP_SENDER` onto the start of
the line and then takes stdin, as fed by NNCP, and appends that to the
line. This should allow NNCP nodes which can run exec handles to be
able to call this script and queue up downloads.

## Limitations
No status updates are sent about whether the download has started or
what progress has elapsed during the download. It should be fairly
simple to incorporate an NNCP sendmail handle to periodically send
progress updates, but as of yet, this does not exist.
