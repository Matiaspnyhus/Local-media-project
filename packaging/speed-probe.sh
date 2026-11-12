#!/usr/bin/env bash
# How fast does this machine convert pictures?
#
# M4 converts a film's picture while it is watched, which only works if the
# PC converts faster than the film plays. This script makes two test films
# (ordinary full HD, and 4K HDR) and times every size and quality setting
# the app may choose, printing each as "film seconds per second": 1.00x is
# exactly real time, and the app wants 1.5x (1.2x for 4K HDR) to be safe.
#
# It is a measuring tool for the people building the app, not part of it.
# At GitHub it runs only when a commit message contains [speed-probe].
#
#   bash packaging/speed-probe.sh [ffmpeg] [work folder] [seconds of film]
set -euo pipefail

FFMPEG=${1:-ffmpeg}
WORK=${2:-$(mktemp -d)}
SECS=${3:-20}
mkdir -p "$WORK"

echo "processors: $(nproc 2>/dev/null || sysctl -n hw.ncpu)"
"$FFMPEG" -version | head -1
echo "--- what this converter has"
"$FFMPEG" -hide_banner -filters 2>/dev/null | grep -E ' (tonemapx|zscale|tonemap|bwdif|subtitles|overlay|libplacebo) ' || true
"$FFMPEG" -hide_banner -encoders 2>/dev/null | grep -E ' (libx264|libx265|h264_nvenc|h264_qsv|h264_amf|h264_vaapi|h264_mf) ' || true

echo "--- making the test films ($SECS seconds each)"
# Moving noise is added so that the films cost about as much to decode as
# real ones of the same size; a clean test pattern is far too easy.
"$FFMPEG" -hide_banner -loglevel error -nostdin -y \
  -f lavfi -i "testsrc2=size=1920x1080:rate=24000/1001:duration=$SECS,noise=alls=12:allf=t" \
  -c:v libx264 -preset veryfast -b:v 8M -maxrate 12M -bufsize 16M -pix_fmt yuv420p "$WORK/sdr1080.mkv"
"$FFMPEG" -hide_banner -loglevel error -nostdin -y \
  -f lavfi -i "testsrc2=size=3840x2160:rate=24000/1001:duration=$SECS,noise=alls=12:allf=t,format=yuv420p10le" \
  -c:v libx265 -preset ultrafast -b:v 25M \
  -x265-params "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:hdr-opt=1:repeat-headers=1:log-level=error" \
  -color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc "$WORK/hdr2160.mkv"
ls -l "$WORK"/*.mkv | awk '{printf "%s  %.1f MB\n", $NF, $5/1048576}'

# measure NAME INPUT ARGS...: the best of two runs, in film seconds per second.
measure() {
  local name=$1 input=$2
  shift 2
  local best=0 try t0 t1
  for try in 1 2; do
    t0=$(date +%s.%N)
    if ! "$FFMPEG" -hide_banner -loglevel error -nostdin -i "$input" "$@" -an -f null - 2>"$WORK/err.txt"; then
      printf '%-52s failed: %s\n' "$name" "$(head -c 300 "$WORK/err.txt" | tr '\n' ' ')"
      return 0
    fi
    t1=$(date +%s.%N)
    best=$(awk -v s="$SECS" -v a="$t0" -v b="$t1" -v x="$best" 'BEGIN{y=s/(b-a); printf "%.2f", (y>x)?y:x}')
  done
  printf '%-52s %6sx\n' "$name" "$best"
}

# x264 SIZE PRESET CEILING: the encoder settings of one "rung".
x264() {
  echo "-c:v libx264 -preset $1 -crf 22 -maxrate $2 -bufsize $(( ${2%M} * 2 ))M -profile:v high -bf 0 -sc_threshold 0 -g 144"
}

echo "--- ordinary full HD film (H.264, 8 Mbit/s)"
measure "decoding only" "$WORK/sdr1080.mkv"
measure "1080p veryfast  (rung 1)" "$WORK/sdr1080.mkv" -vf "scale=1920:1080:flags=bicubic,setsar=1,format=yuv420p" $(x264 veryfast 10M)
measure "1080p superfast (rung 2)" "$WORK/sdr1080.mkv" -vf "scale=1920:1080:flags=bicubic,setsar=1,format=yuv420p" $(x264 superfast 12M)
measure "720p veryfast   (rung 3)" "$WORK/sdr1080.mkv" -vf "scale=1280:720:flags=bicubic,setsar=1,format=yuv420p" $(x264 veryfast 5M)
measure "720p superfast  (rung 4)" "$WORK/sdr1080.mkv" -vf "scale=1280:720:flags=bicubic,setsar=1,format=yuv420p" $(x264 superfast 5M)
measure "480p ultrafast  (rung 5)" "$WORK/sdr1080.mkv" -vf "scale=854:480:flags=bicubic,setsar=1,format=yuv420p" $(x264 ultrafast 3M)

echo "--- 4K HDR film (HEVC 10-bit, 25 Mbit/s), colours brought down to normal"
measure "decoding only" "$WORK/hdr2160.mkv"
for size in 1920:1080 1280:720 854:480; do
  for preset in veryfast superfast ultrafast; do
    measure "tonemapx   ${size/:/x} $preset" "$WORK/hdr2160.mkv" \
      -vf "scale=$size:flags=bicubic,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p" $(x264 "$preset" 10M)
  done
done
for size in 1920:1080 1280:720; do
  w=${size%:*} h=${size#*:}
  measure "zscale+tonemap ${w}x${h} superfast" "$WORK/hdr2160.mkv" \
    -vf "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv,zscale=w=$w:h=$h:t=linear:npl=100,format=gbrpf32le,tonemap=tonemap=hable:desat=0,zscale=p=bt709:t=bt709:m=bt709:r=tv,format=yuv420p" $(x264 superfast 10M)
done
echo "--- done"
