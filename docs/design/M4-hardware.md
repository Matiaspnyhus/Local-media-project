# M4, the graphics card: design draft for the owner's PC

**Status: a draft that has not been revised.** It was written on 2026-11-12 from the research in `M4-hardware-research.md`, after the owner said that the server PC is a Windows laptop with an NVIDIA RTX 3060 and an Intel Core i7. One of two critics (the owner's point of view) finished and found the problems listed at the end; the other (is every command right for the shipped converter?) never ran, and neither did the revision or the building of the hardware test, because the owner's usage ran out. Before anything is built from this: work the critic's problems in, and check every encoder, decoder and filter option against what the shipped converter prints (`ffmpeg -h encoder=h264_nvenc` and so on). It is meant to replace what `M4.md` says about hardware (decisions 1 and 14, the hardware rows and the relaunch rule in section 3, the hardware probe in section 4, acceptance test 3, slice 7).

The staying-awake part (section 5 of the draft) is partly built already, on the line the next version is made from: see `docs/decisions.md`.

## The design

*Replaces, in this document: decisions 1 and 14 (section 1), "Hardware rows" and the relaunch rule (section 3), "Hardware probe" (section 4), acceptance test 3 (section 6) and slice 7 (section 7). Everything else stands: the lattice, the jump recipe, the cutter's 2 ms check, the five rungs, the speed table, lessons.*

The owner's server is a Windows laptop with an NVIDIA RTX 3060 (laptop) and an Intel Core i7 of unknown generation. Nothing here has run on a graphics card: there is none where the app is built or at GitHub. Every claim is marked: **[ran]** = run by me on the pinned jellyfin-ffmpeg 8.1.3-2 (Linux build) on 2026-11-12, outputs in `<scratch>/hw.IWKwVs`; **[source]** = read in FFmpeg release/8.1, Jellyfin's patches for v8.1.3-2, Jellyfin's server code or Microsoft's API pages by the three research helpers; **[inferred]** = reasoning from those, not run; **[unknown]** = nobody could find out, and the hardware test (H.5) is what finds out.

### H.1 Decisions

1. **(replaces decision 1) One software path carries everything and is never switched off.** A graphics-card path is another *row* for the same job: same lattice, same `-noaccurate_seek` + `fps=…:start_time` + `trim` recipe, same tail, same cutter check. A card path is off until a report from this PC passes the rules in H.6, a play uses one path from its first chunk to its last, and any failure hands that play to the software path. Reason: the lattice only works if every run gives every frame the same time; the software path is the one where that was checked on six films.
2. **(replaces decision 14) Designed now, built after the first report from the owner's PC:** decoding, sizing and colour conversion on the NVIDIA card. **Still deferred:** swapping streams on surface change; text subtitles inside the chunk list for AirPlay. **Deferred and named so that nobody builds them by accident:** deinterlacing on the card (`bwdif_cuda`), subtitles laid over on the card (`overlay_cuda`, `alphasrc`), Intel's own tone mapping (`vpp_qsv=tonemap=1`: does nothing and says nothing on 10th-generation chips and older **[source]**), `tonemap_opencl`, AMD (a row with an argument test, never switched on: nobody can try it). **Never:** the `*_cuvid` and `*_qsv` decoders (H.3).
3. **Four card paths exist on this PC; they are tried in this order, then software:** (b) NVIDIA does the whole picture, (a) NVIDIA encodes only, (c2) Intel decodes and sizes, (c1) Intel encodes only. A path is used for a film only if it is on, the film is of a kind it takes (H.2), and its measured speed for that class of film is at least the bar and not below the software path's. Reason for NVIDIA first: its path is the more capable one (tone mapping on the card, every format the films use) and on a 10th-generation chip Intel's encoder is the weaker **[inferred]**; the reasons for Intel first (card asleep, a game running, heat) are plausible but unmeasured, so the test measures start-up time cold and warm and the order is revisited with numbers.
4. **Worth building:** (a) and (b) yes: (b) is what removes the measured limit (a 4K HDR film decodes at 3.7x on four processors before anything else is done); (a) is one row and is what every film (b) does not take falls back to. (c1) is one row and is built with (a), switched on only by its own passed test. (c2) is designed and *tested* in stage one but **not built into the converter** unless the owner's report shows the NVIDIA paths cannot be used or cost seconds to wake: it is the most intricate path (Direct3D decoding mapped to Intel's filters, a download for tone mapping) and second choice on this PC. If the i7 has no usable Intel graphics (an "F" chip, or the laptop set to "NVIDIA only"): Windows lists no adapter of vendor 0x8086, the Intel start run fails with `Failed to find d3d11va adapter by vendor id 0x8086` **[source]**, both Intel paths simply do not exist, and nothing else changes. That state is asked again at every test, never stored as permanent (a laptop's graphics switch or a driver install changes it).
5. **Card decoding only through FFmpeg's own decoders with a hardware helper** (`-hwaccel cuda`, `-hwaccel d3d11va`). Reason: FFmpeg's own code still reads the stream, orders the frames and gives them their times; the card only makes the pixels, so times equal software decoding by construction **[inferred]**. The `*_cuvid` decoders send times through NVIDIA's parser and 100-nanosecond units, and carry no Dolby Vision data; `-hwaccel qsv` swaps in Intel's decoders, which re-round times through 90 kHz and behave differently after a jump **[source]**.
6. **`fps` and `trim` stay first, next to each other, before any card filter** (`setparams` may stand in front: it only relabels). Reason: they are "metadata only" filters that pass card frames untouched and repeat a frame by referencing it **[source]**; Intel's filters set their time base to 1/(frame rate of their input) and round through 90 kHz, which is exact after `fps` and can move a frame by a tick before it **[source]**. `scale_cuda` and `tonemap_cuda` only copy a frame's properties and touch no times **[source: the two Jellyfin patches]**.
7. **NVIDIA key frames:** `-forced-idr 1` (without it a forced frame is a plain intra frame that ffmpeg does not flag as a key frame, and the cutter sees no cut **[source]**); `-bf 0` (decode time equals show time; with B-frames the first decode time depends on where the run began **[source]**); `-rc-lookahead 0` with presets p1 to p5 only (scene-cut key frames exist only with look-ahead; p6 and p7 may switch it on by themselves and ffmpeg then does not pass `-no-scenecut` **[source]**); `-g 10000000`. That last one differs from the software row on purpose: whether a forced key frame restarts NVIDIA's own count is **[unknown]**, and if it does not, `-g <two chunks + 2>` would put an unasked key frame 146 frames after wherever a run began. A count longer than any run is right either way. Whether the driver accepts that number is a probe; if it does not, the fallback is the film's own frame count plus one chunk, or the software value if the probe shows the count does restart.
8. **Intel key frames:** `-forced_idr 1 -idr_interval 0 -adaptive_i 0 -adaptive_b 0 -look_ahead 0 -bf 0 -g <two chunks + 2>`, never `-extbrc`, `-look_ahead_depth` or `-scenario`. Reasons: this build's h264_qsv defaults to three B-frames and a 250-frame count (Jellyfin patch) **[source]**; left unset, Intel's runtime may switch scene-change key frames on **[source]**; in Intel's open-source runtimes a forced key frame restarts the count **[source; the Windows runtime is a closed build of the same family]**. `-g` cannot be made huge here: the field is 16 bits.
9. **Rate.** NVIDIA: `-rc vbr -cq 23 -maxrate M`, no `-bufsize` (ffmpeg throws the buffer size and the average rate away whenever `-cq` is given **[source]**). Intel: `-b:v 0.6·M -maxrate M -bufsize 2·M` (plain VBR, the mode every driver accepts; a target above the ceiling makes Intel silently raise the ceiling, which is the fault in the old row's fixed 6M **[source]**; `-global_quality` with `-maxrate` but without `-b:v` silently becomes fixed quantiser 26 **[source]**). The numbers 23 and 0.6 are starting guesses: there is no dependable mapping from libx264's `-crf` **[reported]**; the test measures size and likeness to the source at three settings and the values are fixed from the owner's report.
10. **No `-level`, and `-profile:v high` always.** NVENC fails outright on a level that does not fit (Jellyfin omits it for that reason) **[source]**; this build's default profile is main **[ran: help text]**.
11. **One path per play.** A header from one encoder never equals another's **[inferred]**, and card and processor scaling or tone mapping do not give identical pictures. What the paths share is the *clock*: the same list of cuts and the same frame times, so a play that falls back is replayed at the same position from the same list (H.4).
12. **Failures are told apart by their words, never by exit code** (here the same build ended with 255, 171, 187, 218, 234 and 244 for different failures **[ran]**; Windows keeps other numbers). The first failure always hands the play to software; how long the path then stays off depends on what kind of failure it was (H.4).
13. **The hardware test has two stages.** Stage one is a report: package `internal/hwprobe`, built now, changes nothing about how films are played. Stage two is the switch, built when a report from the owner's PC is in hand. The rules of H.6 are a proposal to be confirmed against that report.
14. **Staying awake:** a Windows power request (system + execution, never the screen), held while someone is watching and for 15 minutes after, never changing a Windows setting. Windows only for now (H.9).

### H.2 The paths

Symbols as in section 3: A = anchor, P = `Cuts[N]` − max(0.1 s, 3 frames), T = the whole seconds of the cuts, M = the rung's ceiling in kbit/s, W×H = the converted size, G2 = frames in two chunks + 2. "Sound and tail" are exactly the software command's. Preset by rung: rungs 1 and 3 `p4`, rungs 2, 4 and 5 `p1`; Intel has no faster preset than `veryfast`, so on Intel rungs 2 and 4 are the same work as 1 and 3. `Spec.Convert.Encoder` becomes `Convert.Path` (software, nvidiaEncode, nvidiaFull, intelEncode, intelFull, amdEncode), and `Convert.Tone` and `Convert.Burn` choose the variant. Every word below was parsed by the pinned build up to the point where it looks for the hardware **[ran]**.

**(a) NVIDIA encodes only.** Decoding and the whole filter chain are the software row's, unchanged, ending `format=yuv420p`.
```
-hide_banner -nostdin -loglevel error
-ss <Cuts[N] − lead − containerStart> -noaccurate_seek -readrate 8 -readrate_initial_burst 60 -i ABSOLUTE_FILE
-map 0:v:0 -vf "[bwdif=mode=send_frame:parity=auto:deint=interlaced,]fps=NUM/DEN:start_time=<A>,trim=start=<P>,PICTURE[,subtitles=f=s<K>.ass:fontsdir=fonts],format=yuv420p"
-c:v h264_nvenc -preset <p4|p1> -tune hq -profile:v high -rc vbr -cq 23 -maxrate <M>k -bf 0 -g 10000000 -forced-idr 1 -rc-lookahead 0 -no-scenecut 1 -a53cc 0
-fps_mode vfr -force_key_frames T[N],T[N+1],…,T[last]
+ sound and tail
```
**(b) NVIDIA decodes, sizes, converts colour and encodes; frames stay on the card.**
```
-hide_banner -nostdin -loglevel error -init_hw_device cuda=cu:0 -filter_hw_device cu
-hwaccel cuda -hwaccel_output_format cuda -noautorotate -threads 1
-ss <…> -noaccurate_seek -readrate 8 -readrate_initial_burst 60 -i ABSOLUTE_FILE
-map 0:v:0 -vf "[HEAD,]fps=NUM/DEN:start_time=<A>,trim=start=<P>,CARD"
(the encoder line of (a))
-fps_mode vfr -force_key_frames T[N],… + sound and tail
```
- CARD, normal colour (8-bit and 10-bit sources): `scale_cuda=w=W:h=H:interp_algo=bicubic:format=yuv420p,setsar=1`. The `format=yuv420p` inside `scale_cuda` is what brings 10-bit down to the 8 bits the encoder needs; the software `format=` filter cannot follow card frames **[source]**.
- CARD, HDR: HEAD = `setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc` (HLG: `color_trc=arib-std-b67`), CARD = `scale_cuda=w=W:h=H:interp_algo=bicubic,tonemap_cuda=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=yuv420p,setsar=1`, plus `-color_primaries bt709 -color_trc bt709 -colorspace bt709`. Shrink first, as in software. `tonemap_cuda` takes the same option names as `tonemapx` **[ran: both help texts]**; it stops at the first frame that is tagged neither PQ nor HLG and has no Dolby Vision data, hence HEAD, which is only ever added for a film the planner has judged HDR **[source]**.
- With subtitles put into the picture (b′): CARD ends `…:format=yuv420p,hwdownload,format=yuv420p,setsar=1`, then the software chain's own step (`,subtitles=f=s<K>.ass:fontsdir=fonts,format=yuv420p`, or for picture subtitles the software `-filter_complex` with `[0:v:0]…,hwdownload,format=yuv420p,setsar=1[v]` as its first branch). One download of the already small picture; the subtitle text and timing are the software path's, so what was checked there (a caption already showing at a jump) holds.
- `cuda=cu:0`, never a Direct3D adapter number: CUDA lists only NVIDIA devices, so the 3060 is device 0 whichever chip drives the screen **[inferred from source]**. `-threads 1` and no `+unsafe_output`: Jellyfin's setting for a decoder that has no threading of its own; frames are copied off the decoder's surfaces, so later stages cannot run it out of them **[source]**.

**(c1) Intel encodes only** (Windows). The software chain ending `format=nv12` (h264_qsv takes only nv12).
```
-hide_banner -nostdin -loglevel error -init_hw_device d3d11va=dx11:,vendor_id=0x8086 -init_hw_device qsv=qs@dx11
(input and filters as software, last word format=nv12)
-c:v h264_qsv -preset veryfast -profile:v high -b:v <0.6·M>k -maxrate <M>k -bufsize <2·M>k -bf 0 -g <G2> -forced_idr 1 -idr_interval 0 -adaptive_i 0 -adaptive_b 0 -look_ahead 0 -a53cc 0
-fps_mode vfr -force_key_frames T[N],… + sound and tail
```
**(c2) Intel decodes and sizes** (designed and tested; built only if H.1.4 says so).
```
… -init_hw_device d3d11va=dx11:,vendor_id=0x8086 -init_hw_device qsv=qs@dx11 -filter_hw_device qs
-hwaccel d3d11va -hwaccel_device dx11 -hwaccel_output_format d3d11 -noautorotate -threads 2 -ss … -i ABSOLUTE_FILE
-map 0:v:0 -vf "fps=NUM/DEN:start_time=<A>,trim=start=<P>,hwmap=derive_device=qsv,CHIP"
(the encoder line of (c1))
```
- CHIP, normal colour: `vpp_qsv=w=W:h=H:format=nv12:passthrough=0,setsar=1` (`passthrough=0` because the Direct3D decoder's pool of surfaces is fixed in size **[source: Jellyfin]**).
- CHIP, HDR: `vpp_qsv=w=W:h=H:passthrough=0,hwdownload,format=p010le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc,tonemapx=tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709:format=nv12,setsar=1,format=nv12` and the three `-color_*` words. The costly 4K decoding and shrinking move to the chip; tone mapping stays the one filter already chosen, at the small size, so the colours are the software path's and it works on 7th generation and newer alike **[inferred; `tonemapx` from p010 to nv12 ran here]**.

**AMD** (row only): `-c:v h264_amf -usage transcoding -quality speed -profile:v high -rc vbr_peak -b:v <0.6·M>k -maxrate <M>k -bufsize <2·M>k -bf 0 -g <G2> -forced_idr 1`, chain ending `format=nv12`.

**Which films each path takes.** The planner needs two more facts about the picture track than it has: bit depth and chroma format (both in the pixel format ffprobe already reports).

| Film | (b) NVIDIA whole picture | (a) NVIDIA encodes | (c2) Intel whole picture | (c1) Intel encodes |
|---|---|---|---|---|
| H.264 8-bit 4:2:0; HEVC 8- or 10-bit 4:2:0, normal colour | yes | yes | yes | yes |
| HDR10, HDR10+, HLG, Dolby Vision profiles 7 and 8 (10-bit) | yes, `tonemap_cuda` | yes, `tonemapx` on the processor | yes, `tonemapx` after the download | yes |
| Dolby Vision profile 5 | only after the owner's word (H.6); until then (a) | yes (`tonemapx` reads the Dolby data) | no | yes |
| VP9 8/10-bit, AV1 8/10-bit | yes, if the report's decoding table says this card decodes it | yes | VP9 yes; AV1 only where Intel's runtime reports version 2.x | yes |
| H.264 10-bit, anything 4:2:2 or 4:4:4, HEVC 12-bit | no (the 3060 does not decode 10-bit H.264 **[source]**; the others stay out until a sample has been tried) | yes | no | yes |
| MPEG-2, VC-1, MPEG-4 part 2, VP8, anything else | no: light for the processor, and where hardware decoders have gaps | yes | no | yes |
| Interlaced, any format | no: `bwdif` must run before `fps`, on the processor | yes | no | yes |
| Turned (phone video with a rotation) | no: ffmpeg's turning filter cannot take card frames | yes | no | yes |
| Subtitles put into the picture | yes, as (b′) | yes | not built | yes |

Fallback for every "no": the next path in the order that takes the film, and software at the end. The decision is made *before* the run from this table and the report's decoding table, because ffmpeg does not fall back usefully by itself: with a driver present but a format the card cannot decode it prints two error lines, quietly decodes in software, and the card filter then ends the run with "Impossible to convert between the formats" **[source; the filter failure ran here, exit 218]**.

### H.3 Frame timing: the evidence, and the test that proves it on the owner's PC

A path is acceptable only if every run gives every frame the same time as every other run and as the software path. The proving test is the same for every path (ids are the hardware test's, H.5): from two runs from the start and one run jumped into the film, on generated films, (1) every key frame lies on a cut within 2 ms and nowhere else, (2) decode time equals show time for every packet, (3) the list of frame times equals the software run's list, entry for entry, (4) the three headers are byte-identical, (5) a file joined from one run's chunks before a cut and another run's chunks after it decodes without a word of complaint and its pictures match (at least 25 dB in the second before and after the join).

| Path | Why the times should be right | Still unknown, and the probe that settles it |
|---|---|---|
| (a) | Frames reach the encoder from the software chain itself. With `-bf 0` NVENC returns each packet with the time it was given and decode time = show time **[source: nvenc.c]**. For MP4 output the parameter sets are written only into the header, from the session's settings; nothing per-run enters them **[source]**. | Does a forced key frame land exactly, and only there (Jellyfin does not force key frames on NVENC, so its field use proves nothing here)? Does the count restart? Is the header byte-identical between runs on this driver? → `nvidia-encode/sdr/a, b, jump, g146` |
| (b) | The same jump (`-ss` is done by the file reader before any decoder), the same FFmpeg decoder deciding which frames come out and when, `fps`/`trim` untouched by card frames, card filters that copy properties only (H.1.5, H.1.6). | All of it has never run on card frames. The *picture* of the first frames after a jump into a file whose key frames are not clean restart points may differ between card and processor **[inferred]**. → `nvidia-full/sdr/…`, `nvidia-full/hdr/…` (H.264 and HEVC 10-bit, from the start and jumped), `nvidia-full/gaps/jump` (a transport stream with key frames 6 s apart, picture compared) |
| (c1) | As (a); Intel carries times through 90 kHz and back, which is exact for whole ticks of 1/rate **[source]**. | Forced key frames on Intel have no field record either. → `intel-encode/sdr/a, b, jump`; the lines Intel prints about what it really did (`RateControlMethod`, `GopPicSize`, `AdaptiveI`) are kept |
| (c2) | As (b) with Direct3D decoding; `vpp_qsv` after `fps` keeps the time base **[source]**. | Everything. → `intel-full/…` |

What ran here, on the software row with the shipped build: a generated film whose own key frames (every 50 frames) never fall on a cut and whose picture flips every 1.3 s; key frames came at 0, 3.003, 6.006, 9.009 s and nowhere else; a run jumped to chunk 2 gave one lead fragment (3 frames before the cut) and then 6.006 and 9.009; all three headers 832 identical bytes; the joined file decoded cleanly at 42 dB or better per frame against the played-through run, where the same comparison one frame off gives 18 dB. The same harness with the forced key frames left out shows a key frame at frame 146, and with scene detection left on shows one at every flip: so the generated film does provoke both faults the test looks for.

### H.4 When a card path fails

Words in ffmpeg's error text decide the class (the full table of words is in the `hwprobe` specification; the Linux wordings ran here, the Windows ones are read from source).

| Class | Typical words | This play | The path afterwards |
|---|---|---|---|
| no driver | `Cannot load nvcuda.dll` | software | off until the driver or ffmpeg version changes |
| driver too old | `Driver does not support the required nvenc API version` (this build needs 522.25 or newer **[ran: the string is in the binary; source]**) | software | off until the driver version changes; the owner is told the number |
| no card just now | `CUDA_ERROR_NO_DEVICE`, `Failed to find d3d11va adapter by vendor id` | software | off until the next program start or test |
| busy | `OpenEncodeSessionEx failed: out of memory (10)` (the driver's limit on sessions: 3 before driver 530, 5, 8, 12 from 590 **[reported]**), `CUDA_ERROR_OUT_OF_MEMORY` | software | **not** off. No card path for new plays for 2 minutes. Three in 24 hours: off as unreliable until the test passes again |
| bad state | `CUDA_ERROR_UNKNOWN` (seen after sleep on Linux **[reported]**), `device lost`, `GPU Hang` | software | off; one quiet start run 10 minutes later, and at the next program start, switches it back on if it passes |
| settings refused | `InitializeEncoder failed: invalid param`, `… is unsupported` | software | off until ffmpeg, the driver or the app changes |
| this kind of film | `Failed setup for format cuda`, `Hardware is lacking required capabilities`, `Impossible to convert between the formats`, `No decoder surfaces left` | software | stays on; a note for this PC that films of that kind (format, bit depth, chroma, size class) go to (a), kept with the driver version |
| left the lattice | the cutter's own check | software | off until the app or the driver changes: the gravest evidence there is |
| anything else | | software | off until the test passes again |

**(replaces the relaunch rule)** A second launch only when the failed run used a card path and served nothing: the session's spec becomes the software path (straight there, not down the order: one extra start, not two), and `OnPathFailed(path, class, kind of film)` tells the server, which applies the table. A card death after chunks were served answers `ErrEncoderDied` (HTTP 410); the page replays at the same position and the next plan is software. A broken file still starts once. Without this split, "switched off by its first failure" would turn two people watching at once, or a jump that overlaps the old run, into a permanent verdict.

**Caps.** `max(1, cores/4)` stays the rule for software conversions. Card conversions: at most 2 at once until the report's "two at once" figure and the card's memory use say more. Two leaves one session spare for the moment a jump starts a new run before the old one has gone, even on a driver that allows three. `Busy` in the planner counts conversions on the same kind of path.

### H.5 The hardware test, stage one: a report

Package `internal/hwprobe` (its full specification is a separate deliverable). In one sentence: it reads what the installed converter has, makes short films with ffmpeg's own generators (never a file of the owner's), runs the software row as the reference, runs each card path's exact rows from the start, again, and jumped, measures speed and start-up time, compares pictures, and returns plain data: a `Report` with a version number, plus a pure function `Judge(report)` that says what stage two *would* do. It decides nothing.

Server wiring (slice H2): `POST /api/hardware/test` starts it (owner only; refused while anyone is watching or another test runs), `GET /api/hardware` gives progress and the last result, `DELETE /api/hardware/test` stops it. The last four reports are kept as files in the data folder and go into the support bundle as `hardware-report.json` and `hardware-report-1…3.json`; the bundle's README says so. A play that starts during a test cancels the test at once.

The quiet run: once per (app version, ffmpeg version, driver versions), when all of these hold: the program has run for 10 minutes; nobody has watched for 15 minutes; the PC is on mains; no key or mouse for 2 minutes; no test in the last hour. It holds the stay-awake request for its own minutes (reason "Local Media: testing the graphics card"), is cancelled by a play, and is tried at most three times per version.

What a report tells the designers: the Windows build's own lists (accelerators, encoders, filters, option names: the build examined here is the Linux one); card and processor model, driver versions, mains or battery, desktop or service session, remote desktop or not; for every run the exact arguments, time to first fragment, where every key frame landed, header hash, bit rate, and the words ffmpeg and the driver said; per path whether headers agree, whether frame times equal the software run's, how alike the pictures are; speed per path for an ordinary 1080p film and a 4K HDR film, cold and warm start-up, two 4K conversions at once; which formats the card decodes; size and likeness at `-cq` 20, 23 and 26 against libx264 at `-crf 22`.

### H.6 Stage two: the switch

Built when the first report from the owner's PC has been read; `Judge` is then held against that report as a test fixture.

**A path switches itself on** when one report, taken on mains, with nobody watching, without the PC having slept during it and without the card being busy with something else, shows for that path and class of film (ordinary; 4K HDR): the software reference passed its own checks (otherwise the test, not the card, is at fault); every run ended well; key frames exactly on the cuts and nowhere else; decode time = show time; all headers of the path identical; frame times equal to the software run's; the join decodes cleanly at 25 dB or more; the picture against the software path's at 30 dB average and 25 dB at worst; average bit rate no more than 1.25 times the ceiling; speed at least twice the bar (3.0x ordinary, 2.4x 4K HDR) and not below the software path's. The owner sees a notice and a switch to turn it off (H.8). Speeds go into the speed table per path, keyed by ffmpeg version, core count and driver version; a lighter rung than the one measured is never trusted as faster than it.

**A word from the owner is needed** where a machine cannot judge or act: (1) the driver must be updated; (2) the only reports are from battery: plug in and run again; (3) the picture is neither "the same" nor "wrong" (between 20 and 30 dB against the software path's): the path stays off and the designers read the report; (4) Dolby Vision profile 5 on the card: no such film can be generated, so the first one is converted by (a), and the "Why is this converting?" panel offers "Try the graphics card on this film", then asks whether the colours look right. Only a yes puts profile 5 on (b).

**Switched off:** by the first failure, per H.4. **Tried again:** when the driver version changes (read from the registry at every program start, which does not wake the card), when ffmpeg or the app changes, when the owner presses the button; for "bad state" also by itself as in H.4. "Tried again" always means the test for that path runs and must pass. Never a guess on a real play.

### H.7 The report's privacy

Wanted: card and processor model names, driver versions. Never: the computer's name, the user's name, folder paths, anything about the library.

| Field | Where it comes from | How it is cleaned |
|---|---|---|
| the library | nothing | by construction: the package is given two program paths and one work folder; no film, no database |
| arguments of each run | built from whole numbers and fixed words; clips have fixed names and are opened relative to the work folder (`cmd.Dir`) | nothing to clean; a test asserts no argument holds a path |
| card and processor names | display-adapter and processor entries in the Windows registry; `nvidia-smi` with fixed words; ffmpeg's own `GPU #0 - < … >` and `Using device 8086:…` lines | printable characters only, at most 80; then the text scrub below |
| driver versions and dates | registry, `nvidia-smi` | must match digits, dots, dashes, slashes; otherwise dropped |
| what ffmpeg and the driver said | stderr of each run | line by line: the work folder, program folder, home, temp and profile folders become `[test folder]`, `[program folder]`, `[home]`…; any remaining Windows, network or Unix path becomes `[path]` plus its last part (a path with spaces is swallowed to the end of the quote or line: too much rather than too little); the user's and the computer's names become `[user]` and `[computer]` wherever they stand as words; addresses and long tokens as in the log; memory addresses removed; 300 characters a line; only error and warning lines and a fixed list of telling lines are kept, a bounded number per run |
| power, session | Windows calls that return numbers | only words from fixed lists are written |
| last check | the finished JSON | searched for every private text known (names, folders, whatever the server adds); a hit removes all free text from the report, marks it `withheld`, and counts it. A test plants names in a stand-in converter's output and requires none in the report |

### H.8 What the owner sees

All sentences are listed separately (`owner_words`). In short: Settings has "Graphics card" with one button, "Test the graphics card", and a line that it takes three to five minutes, uses clips the program makes itself and changes nothing. Progress is "Step 3 of 7: Converting with the NVIDIA graphics card" with "Stop the test". The result says in one or two sentences whether the card can convert films and how fast against the processor, in stage one adds that nothing has been switched on yet and asks for the support file, and when the card cannot be used says why in ordinary words and what may help (the driver's version found and the version needed; where to get it). Words like encoder, codec, session, hardware acceleration never appear; "driver" is explained once.

### H.9 Staying awake

A laptop left alone goes to sleep with a film still being sent to a phone: Windows does not count that as use **[reported]**.

- **How.** `PowerCreateRequest` with the reason "Local Media: someone is watching a film", then `PowerSetRequest` for `PowerRequestSystemRequired` and `PowerRequestExecutionRequired`; released with `PowerClearRequest`; closed at exit. Through `windows.NewLazySystemDLL("kernel32.dll")` (golang.org/x/sys wraps none of it **[ran: grep]**), calling `Find()` before `Call()` (which panics on a missing procedure) and judging success by the first return value only. A handle, so no thread has to be pinned; `SetThreadExecutionState` belongs to one thread and is wrong from goroutines **[source]**. Never the screen request, never away mode. One owner of the handle, counting 0 or 1.
- **Watching** is `len(Watching()) > 0`. What this feature needs `Watching` to mean: a player that showed a sign of *playing* in the last 90 seconds: a chunk or header request, bytes going out on a direct-play response, or a progress report that is not paused. An open page, a paused film and a finished one are not watching. (Today's `lastStream` is stamped only when a direct-play response *starts*, which would look idle mid-film.)
- **Held** from the first watcher; **dropped** when there has been no watcher for 15 minutes. So a pause keeps the PC awake for 15 minutes and not all night. The wait is ours because Windows 11 may sleep the moment a request is dropped **[reported]**. A setting switches the feature off.
- **After a gap.** A ticker compares clock readings every 10 seconds; a jump of over a minute means the PC slept or the program was frozen. It is logged ("asleep from X to Y; request held: yes/no; on mains: yes/no"), and if someone is watching the request is cleared and taken again, because Windows documents that requests are "terminated" on sleep and does not say they come back **[source]**.
- **Prevented:** sleep after idle time while plugged in. **Not prevented, by any program:** the screen switching off (not asked for; it does not stop the film); closing the lid, the power button, Sleep in the Start menu **[source]**; on battery on a laptop of the newer sleep kind, Windows ends the request five minutes after its sleep timer **[source]**; hibernation at low battery; a maker's policy that ignores such requests. Whether a locked PC honours it is **[unknown]** (PowerToys' documentation says no, Microsoft's reference is silent).
- **What the page says** (sentences in `owner_words`): the setting with two plain warnings (the lid, the battery), the steps to let the lid be closed while plugged in, a banner while someone watches on battery, the tray's tooltip, and, in an already open page that gets no answer, "Cannot reach the PC…" with quiet retries. No wake button is promised: waking over Wi-Fi depends on the laptop's maker and a browser cannot send the packet **[reported]**.
- **The checklist** (`docs/checklists/M4.md`), in the owner's words: (1) plug the laptop in, start a film on the phone, leave the laptop alone for 25 minutes: the film must play through; (2) the same, pressing Windows+L first; (3) the lid steps, then 30 minutes with the lid shut and a film that needs converting, with the warning never to put a running laptop in a bag; (4) send the support file. The bundle gets `power.json`: every hold, release and gap with its time, mains or battery, and numbers read through `powrprof.dll` (lid action, sleep, hibernate and screen timers for mains and battery, whether the laptop is of the newer sleep kind). Numbers only, so no language and no names.
- **GitHub's Windows machine** proves the request is held and released, not that a PC stays awake (a virtual machine never sleeps): the test holds with a reason unique to the run, runs `powercfg /requests` (the runner is an administrator; run directly, not through Git Bash), requires the reason under both `SYSTEM:` and `EXECUTION:`, releases, and requires it gone. If `powercfg` refuses, the test skips, except that the workflow sets `LM_REQUIRE_POWERCFG=1`, which makes a skip a failure. The keeper's timing is tested everywhere with a pretend clock and a pretend holder.
- **Other systems:** nothing is done, and the settings page says so. Nobody runs the server there; a keep-awake that leaks is the complaint other servers get for years; each system needs its own test on a machine that sleeps. The holder is an interface, and the recorded ways in are `caffeinate -i -w <pid>` on a Mac and `systemd-inhibit --what=idle:sleep` on Linux.

### H.10 What can be tested without a graphics card

- **Arguments.** Every row and every probe run against a literal expected list; all typed; no path; the command length.
- **Stand-in converters** (the test binary re-executed, as acceptance test 3 already does). A *replaying* one answers with recorded output, exit code and delay: the no-driver wordings recorded here, the Windows wordings from source, a hang, a megabyte of chatter, planted private names. A *twin* turns each card row into its software equivalent and runs the real ffmpeg, so that the whole judging runs on real bytes on every system at GitHub, and can be told to go wrong on purpose: no forced key frames, scene detection on, B-frames, another size on the jumped run, the anchor moved half a frame, colours swapped, slowed down. Each must be caught under its own name.
- **Judging from recorded outputs.** `Judge` and the failure table as plain table tests; the owner's real reports become fixtures as they arrive.
- **The pinned converter on all three systems at GitHub:** the software reference passes its own checks; the card starts fail in a known class within seconds; the whole test stays under two minutes; on the Windows machine the Intel start is expected to fail with the "no adapter" words, the first sight of the Windows wording.
- **Plumbing end to end:** a test-only path whose rows are the twin's plays a film through planner, spec, rows, cutter and lattice check in the browser test.
- **Only the owner's PC can show:** anything that opens the hardware (see `cannot_be_tested_here`), and, once a path is on, that the iPhone and Chrome play its chunks.

### H.11 Slices (replace slice 7; H1 and A1 do not wait for the converter)

- **H1. `internal/hwprobe`, nothing else touched.** Tests: arguments, both stand-ins, judging, scrubbing, budget and cancelling, the pinned converter on all systems.
- **A1. Staying awake** (`internal/awake`, the keeper, the setting, the texts, `power.json`). Tests: pretend clock and holder (holds at the first watcher, drops 15 minutes after the last, never holds twice, takes again after a gap, off switch); the `powercfg` test.
- **H2. The button, report-only.** Routes, the quiet run, bundle files, the settings section. Tests: owner only; refused while someone watches; one at a time; cancelled by a play; the bundle's no-secrets test with planted names; keyboard reach. Then a release, the owner presses the button, the bundle comes back.
- **H3. Rows in the converter, not reachable.** `Convert.Path`, the row table in `internal/hls/convert.go` (exported so that `hwprobe` uses it and its own copy goes), the failure table, the relaunch rule, the cap. Tests: literal arguments for every row from the start and jumped, normal colour, PQ, HLG, text and picture burn-in; acceptance test 3.
- **H4. The switch.** Path states, `Judge` on stored reports, `Limits.Paths` and speeds per path in the planner, notes per kind of film, the setting, the retry rules, the Dolby Vision question. Tests: state table; a property test that the planner never picks a path that is off, does not take the film, or is slower than software; the end-to-end twin.
- **H5. The owner's PC.** Checklist: the test on mains; an ordinary film and a 4K HDR film on the iPhone and in Chrome with the card on; a jump; two screens at once; a Dolby Vision film if there is one; the stay-awake steps.

### H.12 Acceptance test 3 (replaces)

**A failing graphics card never costs a play.** (i) A session forced to (b) on a machine without the card ends without serving anything, whatever the exit code; the session is served by the software path, `starts == 2`, and the server has the path as "no driver". (ii) A stand-in that dies after two chunks: 410, the next plan is software, and the path is off or not according to the words it died with: "session limit" leaves it on with the two-minute pause, "invalid param" switches it off. (iii) A stand-in that puts a key frame off the lattice: the run ends as "left the lattice" and the path is off. (iv) The hardware test rejects, each by name: an encoder with no key frame at the forced time, one with an extra key frame, one whose jumped run has another header, one whose frames are a tick late, one whose colours are wrong. (v) The broken-file test keeps `starts == 1`. (vi) No text from a stand-in that prints a user's name and folders reaches the report or the bundle.

## The report-only hardware test: specification for its builder

# `internal/hwprobe`: the report-only hardware test (stage one)

Written for a builder who has read none of the research. Build exactly this; where a value is marked *provisional* it is a named constant.

## 1. What it is, and its rules

One new Go package (cgo off) that the server will call later. **Nothing else in the repository changes in this step.** It tries each graphics-card path on short films it makes itself, changes nothing about how films are played, and returns plain data.

- It may import `internal/hls` for three exported things only: `hls.ProbePicture`, `PictureFacts.Lattice()`, `PictureFacts.Size(height)`. It builds its own argument lists: a deliberate copy of the software row and tail of `internal/hls/convert.go` and `hls.go` (`outputArgs`), pinned by literal tests. A later slice moves the rows into `internal/hls` and deletes the copy.
- **Every ffmpeg argument is built from whole numbers and fixed words.** No text from ffmpeg, ffprobe, the registry, the environment or the caller ever becomes part of an argument. Files have fixed names and are opened relative to the work folder: every process runs with `cmd.Dir = WorkDir`, so no path is ever an argument.
- **Every run has a time limit and stops when its context ends:** `exec.CommandContext`, `cmd.WaitDelay = 3 * time.Second`, stdin nil. On Windows hide the window as `internal/hls/hide_windows.go` does (`HideWindow: true, CreationFlags: 0x08000000`) in a build-tagged file of this package.
- Runs are one at a time, except the one "pair" probe.
- stdout is read by the package (limit 96 MB, then the run is stopped and marked "wrote more than expected"); stderr is kept up to 256 KB (first and last 128 KB).
- The package writes only files with the fixed names below, inside `WorkDir`, removes each as soon as nothing needs it, and removes stale ones with those names at start. It never removes a folder.

Files: `hwprobe.go` (Run, order, budget), `rows.go` (argument builders), `clips.go`, `run.go` (process, box reader, progress reader), `judge.go` (parsers, checks, `Judge`), `classify.go` (failure words), `scrub.go`, `report.go` (types), `system_windows.go`, `system_other.go`, `hide_windows.go`, `hide_other.go`, tests.

## 2. Exported API

```go
const ReportVersion = 1

type Options struct {
    FFmpeg, FFprobe string        // the programs as the server found them
    WorkDir     string            // the caller's folder; made if missing
    Budget      time.Duration     // whole test; 0 means 4m30s; capped at 4m50s
    Trigger     string            // "button" or "quiet"
    App         string            // program version, copied into the report
    Private     []string          // texts that must never be in the report; the package adds its own
    PreviousEnd time.Time         // when the last test ended; zero if never
    Progress    func(Progress)    // may be nil
}
type Progress struct{ Stage string; Step, Steps int }
// Stage: "asking", "looking", "clips", "processor", "nvidia", "intel", "checking"

func Run(ctx context.Context, o Options) (*Report, error)
func Judge(r *Report) Verdict     // pure: what the switch (stage two) would do, and what to tell the owner

type Path string // "software", "nvidia-encode", "nvidia-full", "intel-encode", "intel-full"
```
`Run` returns an error only when nothing can be reported: a program path is empty or `-version` cannot be started, `WorkDir` cannot be made or written, or `ctx` is already done. Otherwise it returns a report, also when cancelled (`ended: "cancelled"`) or out of time (`ended: "out of time"`).

Argument builders, exported so the converter can later be pinned against them, all pure:
```go
type Picture struct {
    Num, Den int64   // rate
    AnchorMs int64
    TrimMs   int64   // -1: no trim (a run from the start)
    W, H     int
    Tone     int     // ToneNone, TonePQ, ToneHLG
    Download bool    // card paths: bring the picture down to memory at the end
}
type Rate struct { MaxRateK int; Fast bool; TwoChunks int64 /* frames in two chunks + 2 */ }
func (p Path) Global() []string
func (p Path) Input() []string
func (p Path) Chain(pic Picture) string
func (p Path) Encoder(r Rate) []string
func Colour(tone int) []string
```
`clock(ms)` writes `%d.%03d` (with a leading `-` below zero), as `internal/hls` does.

## 3. The rows (exact words)

Let R = `Num/Den` written `%d/%d`, A = `clock(AnchorMs)`, P = `clock(TrimMs)`, TRIM = `,trim=start=P` when TrimMs >= 0 and empty otherwise, M = MaxRateK, M6 = M*3/5, TONE = `tonemap=bt2390:desat=0:peak=100:t=bt709:m=bt709:p=bt709`, TAG = `setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc` (HLG: `color_trc=arib-std-b67`).

`Global()`
- software, nvidia-encode: none
- nvidia-full: `-init_hw_device cuda=cu:0 -filter_hw_device cu`
- intel-encode: `-init_hw_device d3d11va=dx11:,vendor_id=0x8086 -init_hw_device qsv=qs@dx11`
- intel-full: the same, then `-filter_hw_device qs`

`Input()` (before `-ss` and `-i`)
- software, nvidia-encode, intel-encode: none
- nvidia-full: `-hwaccel cuda -hwaccel_output_format cuda -noautorotate -threads 1`
- intel-full: `-hwaccel d3d11va -hwaccel_device dx11 -hwaccel_output_format d3d11 -noautorotate -threads 2`

`Chain()` (one string, no spaces)
- software and nvidia-encode, normal colour: `fps=R:start_time=A` TRIM `,scale=W:H:flags=bicubic,setsar=1,format=yuv420p`
- software and nvidia-encode, PQ or HLG: `fps=R:start_time=A` TRIM `,scale=W:H:flags=bicubic,tonemapx=TONE:format=yuv420p,setsar=1,format=yuv420p`
- intel-encode: the same two with `format=nv12` as the last word, and `tonemapx=TONE:format=nv12`
- nvidia-full, normal colour: `fps=R:start_time=A` TRIM `,scale_cuda=w=W:h=H:interp_algo=bicubic:format=yuv420p,setsar=1`; with Download: `…:format=yuv420p,hwdownload,format=yuv420p,setsar=1,format=yuv420p`
- nvidia-full, PQ or HLG: `TAG,fps=R:start_time=A` TRIM `,scale_cuda=w=W:h=H:interp_algo=bicubic,tonemap_cuda=TONE:format=yuv420p,setsar=1`; with Download: `…,tonemap_cuda=TONE:format=yuv420p,hwdownload,format=yuv420p,setsar=1,format=yuv420p`
- intel-full, normal colour: `fps=R:start_time=A` TRIM `,hwmap=derive_device=qsv,vpp_qsv=w=W:h=H:format=nv12:passthrough=0,setsar=1`
- intel-full, PQ or HLG: `fps=R:start_time=A` TRIM `,hwmap=derive_device=qsv,vpp_qsv=w=W:h=H:passthrough=0,hwdownload,format=p010le,TAG,tonemapx=TONE:format=nv12,setsar=1,format=nv12`

`Encoder()`
- software: `-c:v libx264 -preset veryfast -crf 22 -maxrate <M>k -bufsize <2M>k -profile:v high -bf 0 -sc_threshold 0 -g <TwoChunks> -forced-idr 1 -fps_mode vfr` (Fast: `superfast`)
- nvidia-*: `-c:v h264_nvenc -preset p4 -tune hq -profile:v high -rc vbr -cq 23 -maxrate <M>k -bf 0 -g 10000000 -forced-idr 1 -rc-lookahead 0 -no-scenecut 1 -a53cc 0 -fps_mode vfr` (Fast: `p1`)
- intel-*: `-c:v h264_qsv -preset veryfast -profile:v high -b:v <M6>k -maxrate <M>k -bufsize <2M>k -bf 0 -g <TwoChunks> -forced_idr 1 -idr_interval 0 -adaptive_i 0 -adaptive_b 0 -look_ahead 0 -a53cc 0 -fps_mode vfr`

Note the two spellings: `-forced-idr` (libx264, NVENC) and `-forced_idr` (Intel).

`Colour(tone)`: for PQ and HLG `-color_primaries bt709 -color_trc bt709 -colorspace bt709`; otherwise none.

Fixed pieces:
- HEAD(level) = `-hide_banner -nostdin -nostats -loglevel <level>`; level is `level+warning` for ordinary runs and `level+verbose` for start and decoding runs (the `level+` prefix makes ffmpeg write `[error]`, `[warning]`, `[info]`, `[verbose]` on each line).
- TAIL = `-an -sn -dn -map_metadata -1 -map_chapters -1 -copyts -avoid_negative_ts disabled -output_ts_offset 1.000 -max_muxing_queue_size 2048 -f mp4 -movflags +frag_keyframe+delay_moov+default_base_moof+frag_discont pipe:1`. So every time in an output is 1,000 ms later than the film's own (`shiftMs = 1000`).
- No `-readrate` anywhere: speed is what is measured.

## 4. The films it makes

Each: `FFmpeg` + `-hide_banner -nostdin -loglevel error -y -f lavfi -i <SOURCE> <ENCODE> -f <FORMAT> <NAME>`. The enable expression keeps its single quotes inside the one argument. After making one, `hls.ProbePicture(ctx, FFprobe, filepath.Join(WorkDir, NAME))` gives its facts, `facts.Lattice()` its cuts and targets, `facts.Size(1080)` (720 for `hlg` and `gaps`) its W and H. Facts and cuts go into the report. A film that cannot be made in its limit is reported and everything that needs it is "not tried".

| id / NAME | limit | SOURCE | ENCODE, FORMAT |
|---|---|---|---|
| `sdr` / `sdr.mkv` | 45 s | `testsrc2=size=1920x1080:rate=24000/1001:duration=10,noise=alls=12:allf=t,negate=enable='lt(mod(t,2.6),1.3)',format=yuv420p` | `-c:v libx264 -preset veryfast -b:v 8000k -maxrate 12000k -bufsize 16000k -profile:v high -g 50 -keyint_min 50 -sc_threshold 0 -bf 2`, matroska |
| `hdr` / `hdr.mkv` | 90 s | `testsrc2=size=3840x2160:rate=24000/1001:duration=6,noise=alls=12:allf=t,format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc:range=tv` | `-c:v libx265 -preset ultrafast -b:v 25000k -x265-params keyint=48:min-keyint=48:scenecut=0:bframes=2:hdr-opt=1:repeat-headers=1:log-level=error`, matroska |
| `hlg` / `hlg.mkv` (extra) | 30 s | `testsrc2=size=1280x720:rate=24000/1001:duration=3,noise=alls=12:allf=t,format=yuv420p10le,setparams=color_primaries=bt2020:color_trc=arib-std-b67:colorspace=bt2020nc:range=tv` | `-c:v libx265 -preset ultrafast -b:v 6000k -x265-params keyint=48:min-keyint=48:scenecut=0:bframes=2:repeat-headers=1:log-level=error`, matroska |
| `gaps` / `gaps.ts` (extra) | 30 s | `testsrc2=size=1280x720:rate=24000/1001:duration=14,noise=alls=12:allf=t,format=yuv420p` | `-c:v libx264 -preset veryfast -b:v 4000k -maxrate 6000k -bufsize 8000k -profile:v high -g 144 -keyint_min 144 -sc_threshold 0 -bf 2`, mpegts |

Why these shapes (ran on the pinned build): the noise makes two different frames differ by picture (18 dB one frame off, against 42 dB for the same frame from two runs); the picture flips every 1.3 s so an encoder that adds key frames at scene changes shows it at once; `sdr`'s own key frames (every 50 frames) never fall on a cut (frames 72, 144, 216), so a source key frame leaking through shows too; `setparams` in the source is needed, since without it the pinned build leaves the file's own colour tags "unknown". Expected for `sdr`: cuts 0, 3003, 6006, 9009 ms, targets 0, 3, 6, 9, TwoChunks 146. Expected for `hdr`: cuts 0, 3003. Measured here on four slow processors: `sdr` 6.7 s (at 12 s length), `hdr` about 28 s.

Decoding samples (extra), each 15 s limit, SOURCE `testsrc2=size=640x360:rate=24:duration=2,noise=alls=12:allf=t,format=<FMT>`, FORMAT matroska, NAME `d-<id>.mkv` (all made here in under a second each):

| id | FMT | ENCODE |
|---|---|---|
| `h264-10` | yuv420p10le | `-c:v libx264 -preset veryfast -b:v 1000k` |
| `hevc-8` | yuv420p | `-c:v libx265 -preset ultrafast -b:v 1000k -x265-params log-level=error` |
| `hevc-12` | yuv420p12le | the same |
| `vp9-8`, `vp9-10` | yuv420p, yuv420p10le | `-c:v libvpx-vp9 -deadline realtime -cpu-used 8 -b:v 1000k` |
| `av1-8`, `av1-10` | yuv420p, yuv420p10le | `-c:v libsvtav1 -preset 12 -b:v 1000k` |
| `mpeg2` | yuv420p | `-c:v mpeg2video -b:v 2000k` |
| `mpeg4` | yuv420p | `-c:v mpeg4 -b:v 1000k` |
| `h264-i` | yuv420p, with SOURCE `testsrc2=size=640x360:rate=48:duration=2,noise=alls=12:allf=t,interlace=scan=tff,format=yuv420p` | `-c:v libx264 -preset veryfast -b:v 1000k -flags +ilme+ildct -x264-params tff=1` |

`sdr` and `hdr` double as the H.264 8-bit and HEVC 10-bit samples. VC-1 has no encoder and is reported "cannot be made".

## 5. Kinds of run

For a film with cuts `Cuts`, targets `T`, lead `LeadMs`, container start `CS` (all from `hls`):

- **from the start** (`a`, `b`): HEAD + Global + Input + `-i NAME -map 0:v:0 -vf Chain(TrimMs: -1)` + Encoder + `-force_key_frames T[1],…,T[last]` (left out when there is none) + Colour + TAIL.
- **jumped** to chunk N (`jump`): the same with `-ss clock(Cuts[N] − LeadMs − CS) -noaccurate_seek` in front of `-i` (left out if not above zero), `TrimMs = Cuts[N] − max(100, 3000·Den/Num)`, and keys `T[N],…`. N is 2 for `sdr`, 1 for `hdr`, and for `gaps` the largest N with `Cuts[N] − LeadMs − CS > 0` that leaves at least 2 s of film after the cut. Worked example, `sdr`: `-ss 4.006 -noaccurate_seek`, `trim=start=5.881`, `-force_key_frames 6,9`.
- **speed** (`speed`): HEAD + `-stats_period 0.25 -progress pipe:1` + Global + Input + `-stream_loop -1 -i NAME -map 0:v:0 -vf Chain(TrimMs: -1)` + Encoder + `-force_key_frames expr:gte(t,n_forced*3)` + Colour + `-an -f null -`. Read `frame=<n>` lines from stdout, stamping each as it arrives. The window opens at the first report with n > 0 and the process is stopped by the package 4.0 s later: `ended: "stopped as planned"`, not a failure. speed = (frames in the window · Den/Num) / seconds of the window; `startupMs` = process start to the window opening. 15 s are allowed for the window to open; limit 20 s.
- **start** (`start`): HEAD(`level+verbose`) + Global + `-f lavfi -i testsrc2=size=1920x1080:rate=24000/1001:duration=2,format=<yuv420p|nv12>` + Encoder(M 10000, TwoChunks 146) + `-force_key_frames 1 -an -f null -`. Limit 20 s. No film needed. (For Intel this is the device check and the first encode in one.)
- **decoding** (`decode/<sample>`): see section 6.
- **comparison** runs: section 7.

Reading a run that writes `pipe:1`: walk the top-level MP4 boxes as they arrive (4 bytes big-endian size, 4 bytes type; size 1 means an 8-byte size follows; size 0 means to the end), noting each box's type, offset, size, and the milliseconds since the process started when its first byte came. Save the bytes as `<run>.mp4` in WorkDir. The **header** is everything before the first `moof`. **Fragments** are the (`moof`, `mdat`) pairs in order. `firstFragmentMs` = arrival of the second `moof` for a run from the start, of the third for a jumped run (its first fragment is the few frames before the cut, which the app throws away), or the end of output if there is none.

A watchdog ticks once a second during every run and compares clock readings taken with `time.Now().Round(0)`; a jump of more than 10 s marks the run `slept: true`: its times are worthless, and it is repeated once if the budget allows.

## 6. The probes, in order

E = always tried while its path is alive and the budget holds. X = extra, in the order given, each only if the time left exceeds its limits. Run limits: from the start and jumped 30 s (45 s on `hdr`), ffprobe 15 s, comparison 30 s.

**Asking the converter** (E, 10 s each): `-hide_banner -version` for both programs (first line kept); `-hide_banner -loglevel error` with each of `-hwaccels`, `-encoders`, `-decoders`, `-filters`, `-init_hw_device list` (names kept, and of encoders, decoders and filters only those containing nvenc, qsv, amf, cuvid, cuda, d3d11, d3d12, opencl, vulkan, hwmap, hwupload, hwdownload, tonemap, bwdif, overlay, scale, vpp, subtitles, alphasrc, libx264, libx265); `-hide_banner -h encoder=h264_nvenc`, `encoder=h264_qsv`, `filter=scale_cuda`, `filter=tonemap_cuda`, `filter=vpp_qsv`, `filter=tonemapx` (only the option names: the first word of lines that begin with spaces and a dash, or with three spaces and a word). A path whose encoder or filters are not listed is "not tried: not in this converter".

**Looking at the PC** (E): section 9.

**Starts** (E): `nvidia/start`; if it ends well, `nvidia/start-again` at once (the difference is the cost of waking the card); `intel/start` (Windows only; elsewhere "not tried: only tried on Windows"). A vendor whose start fails is finished: all its probes are "not tried: the start failed (<class>)".

**The reference** (E): make `sdr`; `software/sdr/a`. If no vendor started, the test ends here (under a minute). Otherwise `software/sdr/jump`.

**NVIDIA encodes** (E): `nvidia-encode/sdr/a`, `/b` (the same again), `/jump`; checks; comparison `picture` against `software/sdr/a`; comparison `join`.

**NVIDIA whole picture** (E): `nvidia-full/sdr/a`, `/jump`; checks; `picture`; `join`. Then make `hdr`; `software/hdr/a`, `software/hdr/jump`; `nvidia-full/hdr/a`, `/jump`; checks; `picture` against `software/hdr/a`; `join`; `nvidia-full/hdr/download-a` (Download true; checks only).

**Speeds** (E): `software/sdr/speed`, `software/hdr/speed`, `nvidia-encode/sdr/speed`, `nvidia-full/sdr/speed`, `nvidia-full/hdr/speed`, `nvidia-full/hdr/download-speed`, `nvidia-encode/hdr/speed`. One second into `nvidia-full/hdr/speed`, sample `nvidia-smi` once more (memory in use during).

**Intel** (E, Windows): `intel-encode/sdr/a`, `/b`, `/jump`, checks, `picture`, `join`; `intel-full/sdr/a`, `/jump`, checks, `picture`, `join`; `intel-full/hdr/a`, `/jump`, checks, `picture`, `join`; speeds `intel-encode/sdr`, `intel-full/sdr`, `intel-full/hdr`.

**Extras**, in this order:
1. `nvidia-encode/sdr/g146`: as `a` with `-g 146` in place of `-g 10000000`. A key frame at 6089 ms (frame 146) and at no other unasked place means NVIDIA's count does *not* restart at a forced key frame; none means it does. Report `gopCountRestarts: true|false|unknown`.
2. `nvidia-encode/sdr/p1`: as `a` with Fast; checks; and its speed run.
3. Decoding tables, NVIDIA then Intel, one run per sample, 15 s each:
   - NVIDIA: HEAD(`level+verbose`) + `-copyts -init_hw_device cuda=cu:0 -filter_hw_device cu -hwaccel cuda -hwaccel_output_format cuda -noautorotate -threads 1 -i S -i S -lavfi [0:v:0]scale_cuda=w=320:h=180:interp_algo=bicubic:format=yuv420p,hwdownload,format=yuv420p[a];[1:v:0]scale=320:180:flags=bicubic,format=yuv420p[b];[a][b]psnr=stats_file=- -an -f null -`
   - Intel: the Intel global and input words of intel-full, and first branch `[0:v:0]hwmap=derive_device=qsv,vpp_qsv=w=320:h=180:format=nv12:passthrough=0,hwdownload,format=nv12,format=yuv420p[a]`
   - The card filter is in the chain on purpose: if ffmpeg quietly falls back to decoding on the processor, the run fails instead of passing. Result per sample: `onCard: true` (exit 0, at least 40 frames compared, average 25 dB or more), `false` with class "this kind of film", or `failed` with its class.
4. Quality and rate: `nvidia-encode/sdr/cq20` and `/cq26` (as `a` with `-cq 20`, `-cq 26`); `nvidia-encode/sdr/vbr` (as `a` with `-b:v <M6>k -maxrate <M>k -bufsize <2M>k` in place of `-cq 23 -maxrate <M>k`); `intel-encode/sdr/qvbr` (as `a` with `-global_quality 23` added, run at `level+verbose`). For each of these, for both `a` runs and for `software/sdr/a`: comparison `source`.
5. `hlg`: make it; `software/hlg/a`, `nvidia-full/hlg/a` (Tone HLG, 720p, M 5000); checks; `picture`.
6. `gaps`: make it; `software/gaps/a`; `nvidia-full/gaps/jump` (and `intel-full/gaps/jump`); checks; comparison `after-jump` against `software/gaps/a` over the second after the cut.
7. `nvidia-full/hdr/pair`: two speed runs started together; both speeds.

Budget: `Run` keeps a deadline of start + Budget. Before each probe: if the time left is under the probe's limits, it and everything after it is "not tried: out of time". Expected: under a minute where nothing starts, about three and a half minutes where everything works on a laptop with a card; on the four-processor machine here the software part alone is about 80 s.

## 7. Checks and comparisons

After each run from the start or jumped that ended with exit 0: `FFprobe -v error -select_streams v:0 -show_entries packet=pts_time,dts_time,size,flags -of csv=p=0 <run>.mp4` gives one line per packet, `pts_time,dts_time,size,flags`; flags beginning with `K` mark a key frame. Film time of a packet = round(pts_time·1000) − 1000.

- **keys**: for a run from the start, every key packet must lie within 2 ms of a different entry of `Cuts`, and every entry must have one. For a jumped run the very first packet must be a key packet earlier than `Cuts[N]` − 2 ms and no more than 1 s before it (the lead), and the rest are judged against `Cuts[N…]`. Report `keysMs`, `missingKeysMs`, `extraKeysMs`.
- **decodeEqualsShow**: `dts_time == pts_time` on every line.
- **timesSame**: the list of `pts_time` strings equals that of the software run of the same film and kind; report the first place they differ.
- **header**: length and SHA-256 of the bytes before the first `moof`. **headersSame** per path and film: `a`, `b` and `jump` (and `download-a`) share one hash. Also report whether a path's hash equals another path's (expected: no).
- **rate**: average kbit/s over the film, and the highest second (packets summed by whole second of film time).

Comparisons, each one ffmpeg run that prints a line per frame on stdout containing `psnr_avg:`, `psnr_y:`, `psnr_u:`, `psnr_v:` (a value may be `inf`); report frames compared, and average and lowest of each, counting `inf` as 100:
- **picture** (X against the reference Y, same clock): HEAD + `-copyts -i X.mp4 -i Y.mp4 -lavfi [0:v:0][1:v:0]psnr=stats_file=- -an -f null -`. `-copyts` is essential: without it ffmpeg moves each input to zero and the frames are paired wrongly.
- **join**: write `join.mp4` = header of `a` + `a`'s fragments 0…N−1 + the jumped run's fragments from its second on. Then HEAD + `-copyts -i join.mp4 -i <a>.mp4 -lavfi [0:v:0]trim=start=S:end=E[x];[1:v:0]trim=start=S:end=E[y];[x][y]psnr=stats_file=- -an -f null -` with S = clock(Cuts[N]) and E = clock(Cuts[N] + 2000) (that is the film's second before and after the cut, on the outputs' clock). Pass: exit 0, no `[error]` line, the expected number of frames, lowest `psnr_avg` at least 25. Not run when the keys check failed. (Here, software: 42 dB or more.)
- **after-jump**: as picture, with both sides trimmed to `start=clock(Cuts[N] + 1000):end=clock(Cuts[N] + 2000)`.
- **source**: HEAD + `-i X.mp4 -i sdr.mkv -lavfi [0:v:0]setpts=PTS-STARTPTS[x];[1:v:0]fps=24000/1001:start_time=0.000,scale=1920:1080:flags=bicubic,setsar=1,format=yuv420p,setpts=PTS-STARTPTS[y];[x][y]psnr=stats_file=- -an -f null -` (no `-copyts` here).

Named constants, *provisional*: same picture = average at least 30 dB and lowest at least 25; wrong picture = average under 20; join floor 25; bars 1.5x (ordinary) and 1.2x (4K HDR); margin 2.0; rate overrun 1.25.

## 8. Failure classes

Decided from the raw stderr, first match in this order; only the class word is stored beside the scrubbed text.

| class | any of these in the text |
|---|---|
| `driver-too-old` | `Driver does not support the required nvenc API version`, `minimum required Nvidia driver` |
| `no-driver` | `Cannot load nvcuda.dll`, `Cannot load nvEncodeAPI64.dll`, `Cannot load nvcuvid.dll`, `Cannot load libcuda.so.1`, `Cannot load libnvidia-encode.so.1`, `Cannot load libnvcuvid.so.1`, `Could not dynamically load CUDA`, `Failed loading nvcuvid` |
| `no-card` | `CUDA_ERROR_NO_DEVICE`, `No CUDA capable devices found`, `No capable devices found`, `Requested GPU` |
| `no-intel-graphics` | `Failed to find d3d11va adapter by vendor id` |
| `intel-runtime` | `Error creating a MFX session`, `Error initializing a MFX session`, `Error initializing an MFX session` |
| `busy` | `OpenEncodeSessionEx failed: out of memory`, `CUDA_ERROR_OUT_OF_MEMORY`, `CreateInputBuffer failed`, `InitializeEncoder failed: out of memory` |
| `bad-state` | `CUDA_ERROR_UNKNOWN`, `device failed (-17)`, `device lost (-13)`, `GPU Hang (-21)`, `Failed to execute:`, `Failed to end frame:` |
| `this-kind-of-film` | `Failed setup for format cuda`, `Failed setup for format d3d11`, `Hardware is lacking required capabilities`, `Impossible to convert between the formats`, `Mapping requires a hardware context`, `No decoder surfaces left`, `Static surface pool size exceeded` |
| `settings-refused` | `InitializeEncoder failed`, `Provided device doesn't support required NVENC features`, `10 bit encode not supported`, `is unsupported`, `Error querying encoder params`, `Error initializing the encoder` |
| `not-in-this-converter` | `Unknown encoder`, `Unrecognized option`, `No such filter`, `Option not found`, `Invalid device type` |
| `time-limit`, `cancelled`, `slept` | set by the runner |
| `unknown` | anything else that failed |

Seen on this machine with the pinned build: NVENC without a driver: `Cannot load libcuda.so.1`, exit 255; the CUDA device line: the same plus `Could not dynamically load CUDA` and `Device creation failed: -1.`; Intel's encoder without Intel graphics: `Error creating a MFX session: -9.` and `Error initializing a MFX session: unsupported (-3)`, exit 171; card filters fed ordinary frames: `Impossible to convert between the formats…`, exit 218. Never judge by the exit number.

## 9. Looking at the PC

Windows, through `golang.org/x/sys/windows` (`NewLazySystemDLL`; call `Find()` before `Call()`, which panics on a missing procedure) and `golang.org/x/sys/windows/registry`. Each read that fails is noted under `system.unread` with a fixed word and the test goes on. (These names are from memory, not run here: verify them on GitHub's Windows machine.)
- mains or battery: kernel32 `GetSystemPowerStatus` (ACLineStatus 1 mains, 0 battery, otherwise unknown; battery percent).
- desktop or service: kernel32 `ProcessIdToSessionId` of the own process (0 = "service").
- remote desktop: user32 `GetSystemMetrics(0x1000)` not zero.
- seconds since the last key or mouse: user32 `GetLastInputInfo` against kernel32 `GetTickCount`.
- processor: `HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`, value `ProcessorNameString`.
- Windows build: `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion`, values `CurrentBuild`, `DisplayVersion`.
- graphics adapters: under `HKLM\SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}` the subkeys `0000` to `0015`: `DriverDesc`, `DriverVersion`, `DriverDate`, `MatchingDeviceId`. Vendor from the id: `ven_10de` nvidia, `ven_8086` intel, `ven_1002` amd, otherwise other. For NVIDIA also the short version: the last five digits of the version with the dots removed, written `NNN.NN` (32.0.15.6094 gives 560.94).
- `nvidia-smi`: only if `%SystemRoot%\System32\nvidia-smi.exe` exists; run it by that full path with `--query-gpu=name,driver_version,memory.total,memory.used,utilization.gpu --format=csv,noheader,nounits`, 5 s limit; one line, five fields split on `, `.

Elsewhere: processor from the first `model name` line of `/proc/cpuinfo` on Linux; `nvidia-smi` from the PATH if there; everything else "unknown".

Always: `runtime.GOOS`, `GOARCH`, `NumCPU`, and seconds since `PreviousEnd`.

## 10. The report (JSON)

Field names are fixed; new fields may be added without changing `version`; a changed meaning raises it.
```
{ "version": 1, "app": "...", "trigger": "button|quiet",
  "started": "RFC 3339, UTC", "tookMs": 0, "ended": "complete|cancelled|out of time",
  "withheld": false, "withheldCount": 0,
  "system": { "os": "", "arch": "", "build": "", "cores": 0, "processor": "",
    "power": "mains|battery|unknown", "batteryPercent": 0, "session": "desktop|service|unknown",
    "remoteDesktop": false, "idleSec": 0, "sincePreviousSec": 0,
    "adapters": [ { "vendor": "nvidia|intel|amd|other", "name": "", "driver": "", "driverShort": "", "driverDate": "" } ],
    "nvidiaSmi": { "found": false, "name": "", "driver": "", "memoryMB": 0, "usedMB": 0, "usedDuringMB": 0, "busyPercent": 0 },
    "unread": [ "" ] },
  "converter": { "ffmpeg": "", "ffprobe": "", "hwaccels": [], "devices": [], "encoders": [], "decoders": [], "filters": [],
    "options": { "h264_nvenc": [], "h264_qsv": [], "scale_cuda": [], "tonemap_cuda": [], "vpp_qsv": [], "tonemapx": [] } },
  "clips": [ { "id": "sdr", "made": true, "tookMs": 0, "bytes": 0, "facts": { ...hls.PictureFacts... },
               "cutsMs": [], "targets": [], "leadMs": 0, "w": 0, "h": 0, "class": "", "said": [] } ],
  "runs": [ { "id": "nvidia-full/hdr/jump", "path": "", "clip": "", "kind": "start|a|b|jump|speed|decode|...", "chunk": 0,
              "args": [], "limitMs": 0, "tookMs": 0, "exit": 0,
              "ended": "ok|failed|time limit|stopped as planned|cancelled|not started|wrote more than expected",
              "slept": false, "class": "", "said": [],
              "firstByteMs": 0, "firstFragmentMs": 0, "fragments": 0, "bytes": 0,
              "packets": 0, "keysMs": [], "missingKeysMs": [], "extraKeysMs": [], "decodeEqualsShow": true,
              "timesSame": true, "firstDifferenceMs": null,
              "headerBytes": 0, "headerSha256": "", "avgKbit": 0, "peakKbit": 0,
              "frames": 0, "windowMs": 0, "speed": 0, "startupMs": 0,
              "onCard": null } ],
  "comparisons": [ { "id": "", "kind": "picture|join|after-jump|source", "a": "", "b": "", "frames": 0, "wantFrames": 0,
                     "avg": 0, "min": 0, "avgY": 0, "avgU": 0, "avgV": 0, "minU": 0, "minV": 0, "exit": 0, "said": [] } ],
  "paths": [ { "id": "nvidia-full", "tried": true, "starts": true, "class": "",
               "headersSame": { "sdr": true, "hdr": true }, "gopCountRestarts": "unknown",
               "speed": { "sdr": 0, "hdr": 0, "hdrDownload": 0, "hdrPairEach": [0, 0] },
               "startupMs": { "first": 0, "again": 0 } } ],
  "decoding": { "nvidia": [ { "sample": "h264-10", "onCard": false, "class": "", "avg": 0 } ], "intel": [] },
  "notTried": [ { "id": "", "why": "" } ] }
```
`args` are kept for every run: by construction they hold nothing private, and they are what a designer needs to repeat a run. The program paths are not stored. Before returning, enforce a 512 KB ceiling by dropping `said` lines of runs that ended well.

## 11. `Judge`

Pure; never run by `Run`. For each card path and each class (`sdr`, `hdr`) it returns `WouldSwitchOn bool` and `Why []string` (plain reasons, one per rule not met), and per vendor an `Outcome` for the owner's sentence: `works`, `no-card-found`, `no-driver`, `driver-too-old` (with the version found and `522.25`), `did-not-answer`, `wrong-results`, `not-faster`, `not-trustworthy` (with `battery`, `slept` or `busy`), `not-tried`, each with the speeds to quote.

A path would switch on for a class when all hold: `ended` is complete, or every run named here ran; `power` is mains; no run `slept`; `nvidiaSmi.busyPercent` under 30 at the start; the software runs of that film pass keys, decodeEqualsShow and headersSame themselves; the path's start is ok; its `a`, `b` (ordinary only) and `jump` end ok with no missing or extra keys, decodeEqualsShow, timesSame; headersSame; join passes; picture is "same"; `avgKbit` is at most 1.25 × the ceiling; speed is at least margin × bar and at least the software speed for that class. For `nvidia-full` `hdr`, `download-a` must pass its checks too. A picture that is neither same nor wrong gives `WouldSwitchOn: false` with the reason "needs a look".

## 12. Privacy

- Collect the private texts once: `Options.Private`; `WorkDir` (absolute), the folders of both programs, `os.UserHomeDir()`, `os.TempDir()`, the values of `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, `HOMEPATH`, `OneDrive`; names: `os.Hostname()`, `COMPUTERNAME`, `USERDOMAIN`, `USERNAME`, the last part of the home folder, and from `os/user.Current()` the Username (both sides of a backslash) and the Name (whole and each word). Names shorter than 3 characters are replaced only inside paths.
- `scrub(line)`, applied to every line from a process and every string read from the PC:
  1. known folders (longest first; also as written with doubled backslashes and with forward slashes; ignoring case on Windows) become `[test folder]`, `[program folder]`, `[home]`, `[temp]`, `[profile]`;
  2. quoted text holding a path separator and starting like a path (`X:\`, `\\`, `/`) becomes `[path]` plus its last part;
  3. unquoted `X:\…` and `\\…` paths: everything up to a quote, `<`, `>`, `|`, `*`, `?`, colon-space or the end of the line, spaces included, becomes `[path]` plus its last part; Unix paths of two or more parts likewise, stopping at a space;
  4. names as whole words, ignoring case, become `[user]` or `[computer]`;
  5. IPv4 addresses become `[address]`; runs of 32 or more of `A-Za-z0-9_-` become `[removed]`;
  6. ` @ 0x` followed by hex digits is removed (it also makes two runs' words comparable);
  7. bytes that are not valid UTF-8 become `?`, control characters go, and the line is cut at 300 characters.
- `said`: keep every line marked `[fatal]`, `[error]` or `[warning]`; of the others only lines containing one of: `Nvenc`, `NVENC`, `GPU #`, `CUDA`, `cuda`, `NVDEC`, `format supported`, `Hardware is lacking`, `Failed setup`, `Lookahead`, `CQ(`, `Using device`, `MFX`, `implementation version`, `Use Intel`, `RateControlMethod`, `TargetKbps`, `MaxKbps`, `GopPicSize`, `IdrInterval`, `AdaptiveI`, `LowPower`, `low power`, `Using the`, `unsupported`, `d3d11`, `D3D11`, `qsv`, `Selecting decoder`. Equal neighbours collapse to one with a count. A failed run keeps its first 20 and last 20 lines; a run that ended well its first 12.
- Names and versions read from the PC: printable ASCII, at most 80 characters, scrubbed; a version or date must consist of digits, dots, dashes and slashes, or it is dropped.
- Last check: marshal the report and search it, ignoring case, for every private text of 3 characters or more. On any hit set `withheld: true`, `withheldCount`, and empty every `said`, every name and `processor`; the numbers stay.

## 13. Tests that need no graphics card

1. **Arguments.** Literal expected lists for: every film; every path's `a` and `jump` on `sdr` and `hdr`; speed, start, decoding and comparison runs. No argument contains a backslash, a drive letter with a colon, the work folder, or a leading slash; the only file names are the fixed ones. The software row equals this literal, which is today's `convertArgs` without `-readrate`, sound and `-loglevel`: `-c:v libx264 -preset veryfast -crf 22 -maxrate 10000k -bufsize 20000k -profile:v high -bf 0 -sc_threshold 0 -g 146 -forced-idr 1 -fps_mode vfr`.
2. **Replaying stand-in** (the test binary re-executed when an environment variable is set, as the repository's fake converter is): answers by the first rule whose words occur in the arguments with recorded stdout, stderr, exit code and delay. Cases: every class of section 8 from its recorded or source wording; a hang (the limit ends it, the class is `time-limit`, and the whole test still ends inside its budget); cancelling mid-run (children gone within 3 s, `ended: "cancelled"`); a megabyte of stderr (bounded); stderr with a planted user name, computer name, home folder, a quoted path with spaces and a network path (none in the JSON; and when one is forced past the scrub, `withheld` is set).
3. **Twin stand-in:** removes the device and hwaccel words, turns `scale_cuda=w=W:h=H:…` into `scale=W:H:flags=bicubic`, `tonemap_cuda=` into `tonemapx=`, drops `hwmap…`, `hwdownload` and `passthrough`, turns the NVIDIA and Intel encoder words into the software ones, and runs the real ffmpeg. With it every card path must pass every check on any machine. Then, each caught by name: no `-force_key_frames` (missing keys, and an extra one at 6089 ms: ran here); `-sc_threshold 0` dropped (extra keys at the flips: ran here); `-bf 2` (decodeEqualsShow false); another size on the jumped run only (headersSame false); the anchor moved by 21 ms (timesSame false); `shuffleplanes=0:2:1` added (picture wrong); `-readrate 0.5` added (speed under the bar).
4. **Parsers and `Judge`** from recorded texts: ffprobe lines, psnr lines with `inf`, progress blocks, MP4 box sequences including a 64-bit size and a cut-off file; table tests for every rule of section 11; reports from the owner's PC become fixtures when they exist.
5. **Budget** with injected run times: essentials first; extras dropped with "out of time"; never past the deadline.
6. **JSON:** the version is present; a round trip keeps everything; unknown fields are ignored on reading; the size ceiling holds.
7. **The real pinned converter, on all three systems at GitHub** (skipped where there is no ffmpeg; uses an unexported switch that makes the films 640x360 and 1280x720 so that it stays quick, arguments otherwise the same): the software reference passes its own checks; the NVIDIA start ends in `no-driver` or `not-in-this-converter`; on Windows the Intel start ends in `no-intel-graphics` or `intel-runtime` and the test logs the exact lines (the first sight of the Windows wording); the registry and power reads return without error; the whole thing takes under two minutes.

## Every sentence the owner would see

- Graphics card
- Some films cannot be shown by a phone or browser as they are, so this PC converts them while you watch. That is heavy work for the processor. A graphics card can do it many times faster. This test finds out whether the graphics card in this PC can.
- Test the graphics card
- The test takes three to five minutes. It uses short test clips that the program makes itself, never your films, and it changes nothing about how films are played.
- This PC is running on battery. Please plug it in before you start: on battery the graphics card is held back, and the result would be too low.
- Someone is watching a film right now, and the test would slow it down. Try again when the film has ended.
- Step 3 of 7
- Asking the converter what it can do
- Looking for a graphics card
- Making the test clips
- Converting with the processor, for comparison
- Converting with the NVIDIA graphics card
- Converting with the Intel graphics
- Checking the results
- Stop the test
- The test was stopped. Nothing was changed.
- The test stopped because someone started watching a film, so that it would not slow the film down. Run it again later.
- Your NVIDIA graphics card can convert films: about 14 times faster than playing speed for ordinary films, and about 6 times faster for 4K films in HDR (films with extra-bright colours, made for newer TVs).
- With the processor alone, this PC manages about 5 times and about 2 times.
- Nothing has been switched on yet. For now the processor still does the converting, as before. The graphics card will be switched on in a later version, once these results have been looked at.
- Please send the results: choose "Download support file" below and attach the file to the chat. It holds the model of your graphics card and processor, the version of the graphics driver, and what the test measured. It holds nothing about your films, no names and no addresses.
- The Intel graphics in this PC can convert films too: about 9 times faster than playing speed for ordinary films, and about 3 times for 4K films in HDR. It would be used when the NVIDIA card cannot be.
- Your NVIDIA graphics card could not be used, because its driver is too old for this program. (The driver is the software from NVIDIA that lets Windows use the card. Yours is version 472.12; version 522.25 or newer is needed.) So the processor does the converting, which works but is slower for 4K films.
- Updating the graphics driver should fix this: open the NVIDIA program on this PC (it is called "NVIDIA app" or "GeForce Experience"), choose Drivers, and install the newest one. Or download it from nvidia.com/drivers. Then run this test again.
- This PC has an NVIDIA graphics card, but its driver is missing or not working, so the processor does the converting, which works but is slower for 4K films. Installing the graphics driver from nvidia.com/drivers may help. Then run this test again.
- The NVIDIA graphics card did not answer. It may be switched off in this laptop's own settings. So the processor does the converting, which works but is slower for 4K films. Restarting the PC may help. Then run this test again.
- Your NVIDIA graphics card started, but the test clips did not come out the way they must for films to play properly. So the processor does the converting, which works but is slower for 4K films. Updating the graphics driver may help. If it does not, please send the support file: this is something for the makers of the program to look into.
- Your NVIDIA graphics card works, but on this PC it is not faster than the processor (about 4 times playing speed against about 5), so the processor keeps doing the converting.
- No graphics card that can help was found in this PC, so the processor does the converting. That works: ordinary films convert at about 5 times playing speed. 4K films may be shown at a smaller size so that the PC can keep up.
- The graphics card was busy with something else during the test (a game, perhaps), so the result cannot be trusted. Close other programs and run the test again.
- This test ran on battery, where the graphics card is held back. Plug the PC in and run the test again to get the real figures.
- The PC went to sleep during the test, so the result cannot be trusted. Run the test again.
- The test took too long and was stopped. Nothing was changed. Run it again when the PC is not busy.
- The converter that comes with this program could not be found, so nothing could be tested. Installing the program again should fix this.
- The Intel graphics in this PC could not be used. That does no harm: it is only the second choice.
- Windows shows no Intel graphics on this PC at the moment.
- The driver for the Intel graphics is too old. Windows Update, or the support page of the laptop's maker, has a newer one.
- Last tested on 13 November at 19:02.
- The graphics card was tested while the PC had nothing to do.
- The graphics card now helps to convert films.
- Use the graphics card to convert films
- Recommended. Switch this off if films look wrong, or if the laptop gets too loud or too warm.
- The graphics card had a problem. Carrying on with the processor.
- The graphics card stopped working while a film was playing, so the processor took over and the film went on. The graphics card stays switched off until the test passes again. Updating the graphics driver may help.
- The graphics driver has changed since the last test. The test will run by itself the next time the PC has nothing to do.
- This film stores its colours in a special way (Dolby Vision). The graphics card has not been tried on a film like this yet, so the processor is converting it.
- Try the graphics card on this film
- Do the colours look right?
- Yes, they look right
- No, they look wrong
- Good. The graphics card will be used for films like this one.
- The processor will keep converting films like this one.
- Keep this PC awake while someone is watching
- While a film is playing on any phone, tablet or computer, Local Media asks Windows not to put this PC to sleep. Fifteen minutes after the last film is paused or has ended, Windows may put it to sleep again as usual. The screen may still switch itself off; that does not stop the film.
- Two things can still stop a film:
- Closing the lid. Windows then puts the laptop to sleep, whatever any program asks. If you want films to keep playing with the lid closed, you can change that in Windows. The steps are below.
- Running on battery. To save the battery, Windows puts the laptop to sleep after a few minutes, even in the middle of a film. Keep the laptop plugged in while films are being watched.
- To let films play with the lid closed
- Keep the laptop plugged in. Open Settings, then System, then Power & battery. Open "Lid, power & sleep button controls". At "Closing the lid will make my PC", in the box under "Plugged in", choose "Do Nothing". Leave the box under "On battery" as it is.
- If you cannot find that: open the Start menu, type Control Panel and open it. Choose Hardware and Sound, then Power Options, then "Choose what closing the lid does" on the left. In the row "When I close the lid", under "Plugged in", choose "Do nothing". Then press "Save changes".
- The names may be a little different on your version of Windows.
- With this setting the laptop keeps running, and gets warm, with the lid shut. Do not put it in a bag while it is switched on.
- If the PC still goes to sleep during a film while it is plugged in: open Settings, then System, then Power & battery, then "Screen, sleep, & hibernate timeouts" (on older versions of Windows: "Screen and sleep"). Under "Plugged in", set "Make my device sleep after" to "Never". The screen can still switch itself off.
- Keeping this PC awake: someone is watching.
- Not keeping this PC awake: nobody is watching.
- This PC is running on battery. Windows may put it to sleep in the middle of a film. Plug it in.
- Local Media is running. Keeping this PC awake: someone is watching.
- Local Media could not ask Windows to stay awake. Films still play, but the PC may go to sleep during a long one.
- On this kind of computer Local Media does not keep the computer awake. Set the computer's own sleep settings so that it stays on while films are being watched.
- Cannot reach the PC. It may be asleep, switched off, or off the home network. Wake it (open the lid or press a key), then try again.
- Trying again by itself...

## Open questions

- How does stage one reach the owner? The working branch carries packaging/NOT-FOR-RELEASE.txt, and only the newest commit can be published. The report-only button (slices H1 and H2, and A1) has to sit in a release the owner installs before any card path can be designed further. The lead must decide which commit carries it and what unfinished M4 work is reverted for that one commit.
- hwprobe keeps its own copy of the software row and of the tail (outputArgs), because the task forbids touching internal/hls in that step and convertArgs is not exported. The HDR software row (slice 3) is not merged yet, so the probe's copy of it is written from the design and may differ from what slice 3 lands. Accept the copy until slice H3 exports the rows, or export them now?
- -g 10000000 for NVENC: I could not find out whether the driver accepts so large a count, nor whether a forced key frame restarts its count. The probe tests the large value and, as an extra, the software value 146. If the large value is refused: use the film's own frame count plus one chunk, or 146 if the count is shown to restart.
- -cq 23 (NVIDIA) and the Intel target of 0.6 times the ceiling are starting guesses with no source. They are to be fixed from the owner's report (size and likeness at cq 20, 23, 26 against libx264 at crf 22; Intel's QVBR run). With -cq the buffer size is discarded by ffmpeg, so short bursts above the ceiling are the driver's business; if the measured average exceeds 1.25 times the ceiling, the plain VBR line is the fallback.
- Is the Intel whole-picture path (c2) to be built at all? Proposed: only if the owner's report shows the NVIDIA paths unusable or seconds slow to wake. It is tested in stage one either way. If the i7 is 10th generation or older, Intel has no tone mapping of its own and no AV1.
- Thresholds for "the same picture" (30 dB average, 25 dB at worst) come from software runs here (40 dB and more between two encodes, 18 dB one frame off). Nobody knows what tonemap_cuda against tonemapx gives; the HDR threshold must be set from the first report, and until then an HDR result between 20 and 30 dB counts as "needs a look".
- Should a path that passes switch itself on with a notice and an off switch (proposed), or wait for the owner to tick a box? The design's old wording was "off until the owner's test passes"; the proposal keeps that and adds no question.
- Dolby Vision profile 5 on the card needs a look by the owner at a real film (no such film can be generated). Proposed: a small question in the "Why is this converting?" panel. Is that acceptable, or should profile 5 simply stay on the processor's tone mapping?
- Card conversions at once: 2 is a guess until the report's two-at-once figure, the card's memory use and the driver's session limit are known. A separate probe that opens sessions until one fails was left out of stage one as too heavy; the limit is read off the driver version instead (3, 5, 8 or 12, as Jellyfin's documentation tables it).
- Watching() is assumed. Staying awake needs it to mean "showed a sign of playing in the last 90 seconds" and to leave out paused players and open pages. If the update fix defined it otherwise (for example: counts a paused film), the 15-minute rule has to move into Watching or into the keeper. Is 15 minutes after a pause the right length?
- The quiet test run wants the PC idle (no key or mouse for 2 minutes) and holds the stay-awake request for its own few minutes, so that Windows does not sleep in the middle of it. Is it acceptable that the program keeps the PC awake for a test nobody asked for? Without it a laptop on default timers would often sleep before the test ends.
- Staying awake on Mac and Linux: proposed to do nothing for now and say so on the page. The research recommended caffeinate and systemd-inhibit; I left them out because nobody runs the server there and neither can be tested on a machine that really sleeps.
- The lid instructions quote Windows' English labels, taken from guides, not from a PC. On a Windows in another language, or an older build, the names differ; the checklist should say "look for something like".
- A finding outside this task: packaging/speed-probe.sh makes its HDR film with -color_trc and -color_primaries as output options. On the pinned 8.1.3 build that leaves the file's own tags "unknown" (ffprobe: color_transfer=unknown, color_primaries=unknown; the frames inside are tagged PQ, so the speeds measured in section 8 stand). The app's library reads the file's tags, so a test film made that way would be classed as normal colour. Acceptance test 1's generated 4K HDR film must put setparams into the source chain, as the probe's clip does (checked here: tags then read smpte2084 and bt2020).
- Acceptance test 3 as written says a session forced to h264_nvenc "exits 255 on every runner". On the pinned Linux build NVENC without a driver does exit 255, but Intel's encoder exits 171 and device lines 187 or 244, and a Mac build may not have the encoder at all. The replacement test does not look at the number.
- I could not find out which clock Go's timers follow across sleep on Windows (whether a time limit keeps running while the PC is asleep). The design does not rely on either answer: a watchdog compares clock readings and marks a run as "slept".
- The Windows registry key and value names for display adapters and the processor, the NVIDIA version arithmetic (32.0.15.6094 means 560.94), and the user32 and kernel32 calls for session, remote desktop and last input are written from memory. They are cheap to verify on GitHub's Windows machine in slice H1, and each failed read is recorded instead of stopping the test.
- Whether PowerRequestExecutionRequired taken by the server also covers the ffmpeg it starts when the screen is off on a laptop of the newer sleep kind is not stated anywhere. The owner's 25-minute test with a film that needs converting answers it; if chunks stop while the server is awake, ffmpeg needs its own answer.
- Rungs 2 and 4 use NVENC preset p1 and Intel has no faster preset than veryfast. With card speeds expected far above the bar the ladder on the card is about quality and heat, not about keeping up. Should the card get its own, shorter ladder (1080p, 720p, 480p) instead of five rungs?

## What cannot be tested where the app is built

- Any run that opens NVIDIA's hardware: that h264_nvenc starts on this laptop (driver version, two graphics chips, desktop session), that forced key frames land exactly on the cuts and nowhere else, whether a forced key frame restarts the encoder's count, whether -g 10000000 is accepted, whether two runs give byte-identical headers on this driver.
- That fps, trim, setparams and setsar really pass frames that live on the card, and that frame times with -hwaccel cuda equal software decoding from the start and after a jump (H.264 and HEVC 10-bit; and the picture after a jump into a transport stream with key frames 6 s apart).
- tonemap_cuda's picture against tonemapx's (PQ and HLG), 10-bit to 8-bit in scale_cuda, hwdownload after the card filters, and every speed: ordinary 1080p, 4K HDR, with a download, two at once.
- Dolby Vision on the card (profiles 5 and 8): no such film can be generated anywhere; only a real film on the owner's PC and the owner's eyes can show it.
- Which formats this card and this Intel chip decode (the decoding table), including 12-bit HEVC, VP9, AV1 and interlaced H.264; VC-1 cannot even be generated.
- Everything about Intel on Windows: the d3d11va and qsv device lines (the Linux build has no d3d11va, so they cannot even be parsed here), which generation and runtime the i7 has, forced key frames on h264_qsv, vpp_qsv after fps, the download-and-tonemapx HDR chain, which rate modes the driver accepts.
- The Windows build's own lists (accelerators, encoders, filters, option names) and the Windows wording and exit codes of every failure. GitHub's Windows machine can show the lists and the "no Intel adapter" and "no NVIDIA driver" wordings; the rest only the owner's PC.
- The cost of waking the NVIDIA card when it has been idle, behaviour on battery, after sleep and resume, with a game running, in a remote-desktop session, and how warm and loud the laptop gets.
- The driver's session limit and the card's memory per conversion on this driver.
- That an iPhone (Safari) and Chrome play chunks made by NVENC or Intel's encoder (profile high, level chosen by the driver): here only ffmpeg's own decoder reads them.
- Staying awake for real: that the request keeps this laptop awake on mains with the screen off, what happens on battery, when locked, with the lid closed, and whether a request ended by Windows comes back after waking. GitHub's Windows machine proves only that the request is held and released (it is a virtual machine and never sleeps).
- The Windows calls and registry reads the package uses for power source, session, last input, adapter names and driver versions, and nvidia-smi: no Windows here. GitHub's Windows machine can run them, without an NVIDIA or Intel adapter to find.
- hls.ProbePicture and the lattice on the generated films with the Windows build of ffprobe (ran here only by hand with the Linux build: key frames at 0, 3.003, 6.006, 9.009 s; headers identical; join clean).
- The time the whole test takes on the owner's laptop. Measured here on four slow processors: the ordinary test film in 6.7 s, the 4K HDR one in about 28 s, the software reference runs in about 30 s; the card runs are estimates.

## The critic's problems (owner's point of view), not yet worked in

#### A card run that hangs, or is only slow tonight, never hands the film to the processor

*Severity: high.*

**Evidence.** READ (design): H.4 starts a second run only when "the failed run used a card path and served nothing" and tells failures apart "by their words". The table for real plays has no row for a run that says nothing and does not end (the probe has `time-limit`, Judge has `did-not-answer`; the play table has neither). READ (code): a chunk request waits 30 s, answers "the converter did not produce this part in time" and leaves the run alive, so the next request waits on the same run (internal/hls/hls.go, func wait, lines 470-620). Stopping a run waits for it without limit (`halt`: `<-r.exited`, lines 987-1011), and Go's Cmd.Wait blocks in Process.Wait until the process is really gone, WaitDelay or not (go1.26 src/os/exec/exec.go line 930); the same holds for hwprobe's Run, which the server lets run "one at a time". INFERRED, not run: a process stuck inside a driver call is the case where ending it can fail (rare). SLOW WITHOUT AN ERROR, READ: StepDown asks for the next lighter rung on the same encoder, at most two steps a play (internal/playback/stepdown.go, maxSteps = 2), and H.1.3's "not below the software path's" is judged from the stored table, measured on mains in a 4-second window. No rule in H.1 to H.12 sends a play to software because the card is slow now. WALK-THROUGH (inferred; real speeds under a game are unknown): a game holds the card, or the laptop is on battery, or hot with the lid shut. A 4K HDR film stalls, steps down twice on the same card, stalls again and ends at "nothing can be done"; the processor alone would have shown it at 720p (2.2x on four processors, docs/design/M4.md section 8). The page's own offer "lighter picture" plans the same card path again. The lowered figure then stays in the PC's table for 30 days (M4.md section 4).

**Suggested.** (1) A start deadline for card runs: no kept fragment within N seconds (N from the report's cold start-up figure) = kill, class "did not answer", software relaunch if nothing was served, else 410. (2) A bounded stop in both the manager and hwprobe: a run that has not ended a few seconds after the kill is abandoned, that vendor is "stuck" and unused until the program restarts, and Run returns a report saying so. (3) Pace under the bar on a card path = replay on software ("slow tonight": a pause of minutes, not a switch-off, and no 30-day entry in the speed table). (4) Acceptance tests with a stand-in that prints nothing and never ends, and one that crawls.

#### A device that cannot show the card's picture is taught a lesson about itself, and a path turns itself on before any device has played its chunks

*Severity: high.*

**Evidence.** READ: internal/playback/stepdown.go, DeviceLesson.MaxHeight: "set when the trouble was with a picture this PC converted, which is as easy to show as a picture gets: the device is sent nothing larger than this from then on". That sentence is true of libx264 output and unproven for NVENC or Intel output. H.6: "A path switches itself on" from one passing report; slice H4 (the switch) comes before H5 (the owner's checklist); `cannot_be_tested_here`: "That an iPhone (Safari) and Chrome play chunks made by NVENC ... here only ffmpeg's own decoder reads them". WALK-THROUGH (inferred): the first card play on the iPhone fails to decode, or shows a wrong picture after a driver change. The player reports decode trouble; the server lowers that device's size for 30 to 120 days and plans the next try on the same card path; after two steps the film is dead. For a wrong picture the only remedy is the owner's switch in Settings (owner_words: "Switch this off if films look wrong"); a viewer who is not the owner has none, and "Colours look wrong on this device" (M4.md section 5) records a lesson about the device. The processor would have played the film. How likely NVENC output is to fail on Safari: I could not find out (Jellyfin serves NVENC H.264 to iPhones, but without forced key frames or `-g 10000000`).

**Suggested.** Treat a path as on trial per kind of device until one play there has ended well. In a card play, decode trouble or "looks wrong" replays on software first and counts against the path, never against the device. Give every viewer "The picture looks wrong" for a card-made picture (software for this play, a note for the owner). Let a path switch itself on only after the owner's supervised plays (H5), or keep the notice saying "on trial" until then. Add both cases to acceptance test 3.

#### If the driver, the program or the PC goes down during the test, no report exists, and the quiet run tries again

*Severity: high.*

**Evidence.** READ: probe section 2, `Run` returns the report at the end; H.5, "The last four reports are kept as files" by the server afterwards; nothing is written while the test runs. The quiet run is "tried at most three times per version". So for the gravest outcomes (a display-driver reset that takes the program with it, a blue screen, a PC that hangs and is switched off, the program killed) the designers get nothing, cannot tell which run was in flight, and the same run is started again unasked up to three times. The task asked whether the report is enough "for every failure": for this one it is absent. Whether these runs can crash this driver: unknown, nothing has run on a card.

**Suggested.** Write the report file after every run, and a small "in flight: <run id>, started <time>" marker before every run that opens hardware; remove it after. At program start a leftover marker becomes the last entry of that report ("the program or the PC went down during nvidia-full/hdr/pair"), goes into the bundle, marks that vendor "not trustworthy", and stops quiet runs for that version; only the owner's button tries again. Count Windows' own display-driver reset events during the test as a number in the report if they can be read without names.

#### The report's last privacy check empties the driver's words for ordinary account names

*Severity: high.*

**Evidence.** READ: probe section 12, last check: "marshal the report and search it, ignoring case, for every private text of 3 characters or more. On any hit ... empty every `said`, every name and `processor`". The names include USERNAME, the last part of the home folder and each word of the account's display name. RAN (<scratch>/hw.O6hs74/py/lastcheck.py, the report's own field names and arguments copied from the specification, no text from any PC): of 179 common first names and account words, 34 are found inside the report's fixed vocabulary, for example Max (`-maxrate`), Sam and Tim (`timesSame`), Ann and Anne (`stopped as planned`), Marie (`color_primaries`), Rob (`ffprobe`), Eve (`level+verbose`), Per (`batteryPercent`), Tor (`-noautorotate`), Pat (`path`), Win (`windows`), Test (`testsrc2`). The placeholders hit too: an account called User, Home or Temp matches `[user]`, `[home]`, `[temp]`. The list of names is mine, so "about one household in five" is rough. On a hit the match stays in the file (arguments and field names are not emptied) while the only text a designer can act on is removed: with Windows wordings unseen, most failures will be class `unknown`, and an `unknown` without words says nothing. No remedy exists for the owner.

**Suggested.** Search only the free text (`said`, names, `processor`), by whole words as rule 4 does, after taking out the report's own vocabulary and placeholders. On a hit drop that one line and count it; never empty everything. Add tests with accounts named Max, Sam, User and Test that require the driver's words to survive.

#### The owner is told the support file holds nothing about their films; it holds film titles

*Severity: high.*

**Evidence.** owner_words: "choose 'Download support file' below and attach the file to the chat. It holds the model of your graphics card and processor, the version of the graphics driver, and what the test measured. It holds nothing about your films, no names and no addresses." READ (internal/server/routes.go, handleSupportBundle, lines 426-493): the file also holds `not-found-online.json` (up to 2,000 titles "as read from file names"), `playback-problems.json` with the names of files that failed to play, `devices.json`, counts, and 512 KB of log; its own README says "the names of files that failed to play". The design adds `power.json` with "every hold, release and gap with its time": numbers only, but a record of when the household watches and when the laptop is opened and shut, with no limit stated.

**Suggested.** Either give the test its own download ("Download the test results": only the hardware reports) and keep the sentence, or make the sentence say what the support file really holds. Limit `power.json` to the last days or entries and name it in the bundle's README and in the sentence.

#### One odd film, one unseen Windows wording or one evening with a game switches the card off until the owner presses the button

*Severity: medium.*

**Evidence.** READ: H.4, "anything else: off until the test passes again"; H.6, a path is tried again by itself only for "bad state", otherwise when a version changes or "when the owner presses the button". Every Windows wording except two is unseen (`cannot_be_tested_here`), so `unknown` will be the common class at first. "This kind of film": one file's words send every film of that "format, bit depth, chroma, size class" to (a), "kept with the driver version"; for HEVC 10-bit 4K that is the whole gain, lost to one odd or damaged file. M4.md section 4 already has the better rule ("the first failure counts for that file, a second file for the kind"). "Busy": three in 24 hours = "off as unreliable"; other programs hold encoder sessions too (limit 3 on drivers before 530, research), so a game evening or two viewers who jump about can reach three. "No card just now": off "until the next program start or test"; ASUS describes a mode that removes the NVIDIA card whenever the laptop is on battery (rog.asus.com, "maximize your ROG laptop's performance with a MUX switch", seen as a search summary), and a tray program on a laptop is not restarted for weeks. After a wake: a card run frozen across sleep may die with words nobody has seen; the research asked for a "does NVENC survive sleep" probe and the probe package has none. READ (code): a real run keeps only the last 4,096 bytes of what ffmpeg said (hls.go lines 1046-1059), so the first, telling line can be gone when the class is decided. None of this makes the owner worse off than with no card path; it loses the card silently and makes its return a chore.

**Suggested.** File first, kind only after a second, different file. `unknown`: software now, then the path's short test by itself at the next quiet moment (a few times a day at most), not the button. Busy never becomes off: lengthen the pause or lower the cap to one, and tell the owner another program is using the card. "No card": ask again when the power source changes and before the next card play. A card run that dies within a minute of a detected gap is "after sleep" (bad-state treatment) whatever its words. When the software relaunch of the same film fails too, the card is acquitted. Keep the first and the last lines of a real run's output.

#### Watching() already exists and means something else: the PC would be kept awake for hours and the test button refused

*Severity: medium.*

**Evidence.** H.9: "Watching is `len(Watching()) > 0`", needing "a sign of playing in the last 90 seconds" with "a paused film and a finished one are not watching"; the open question says "Watching() is assumed". READ: it was merged while this review ran (commit e473e3f, internal/server/watching.go lines 17-64): a sign-in is watching for 15 minutes after its last sign (`quietFor`), a progress report on pause is a sign, and a repackaged or converted film that is still open counts for 4 hours (`pausedFor`), because "the server cannot tell a finished repackaged film from a paused one". With the keeper's own 15 minutes on top the request is held 30 minutes after a film sent as it is, and up to 4 h 15 min after a converted film whose page is left open on a phone. That is the leak H.9 itself calls "the complaint other servers get for years". The owner's sentence "Fifteen minutes after the last film is paused or has ended" would be untrue. The same call gates the test: the button would answer "Someone is watching a film right now" for up to four hours after a film ended, and the quiet run would wait as long.

**Suggested.** Give the keeper and the test their own signal (a chunk, bytes of a direct play or an unpaused progress report in the last 90 seconds), kept beside the update guard's deliberately generous one, with a test that a paused and a finished converted film release the request after 15 minutes. Let the refusal sentence tell paused from playing.

#### The quiet test can start under someone using the laptop, and in stage one it buys nothing

*Severity: medium.*

**Evidence.** READ: H.5 conditions are program up 10 minutes, nobody watching through the app for 15, mains, "no key or mouse for 2 minutes", once per version, at most three tries. (a) Two minutes without key or mouse is normal while watching a film in another program, in a video call, or playing with a game controller (that `GetLastInputInfo` ignores controllers is from memory; I found no source). The app's own "Open in another player" on the server PC is not a sign of watching: `filmRoutes` (watching.go lines 71-79) has neither `open-local` nor `handoff` (routes.go lines 45-47). The test then makes a 4K film with libx265 and loads the card for three to five minutes. (b) INFERRED: on a laptop that sleeps after five idle minutes, the likeliest moment is shortly after the evening's film, at night, possibly with the lid shut after the owner followed the lid steps: full load and fans that nobody asked for. (c) It is the first contact with untried hardware runs, unattended (see the finding on a test that goes down). (d) In stage one a report is of use only when the owner sends the file, which they are asked to do after pressing the button. (e) Whether a try cancelled by a play counts toward the three is not said; if it does, the sentence "The test will run by itself the next time the PC has nothing to do" stops being true after three evenings.

**Suggested.** No quiet run in stage one. In stage two use it only to re-check a path that has already passed once on this PC (after a driver or converter change), never as first contact. Add conditions: no full-screen or presentation state (Windows has a call for this, `SHQueryUserNotificationState`; from memory), the processor mostly idle over the two minutes, lid open, not between 22:00 and 08:00. Count only tries that ran to the end or went down. Say in the settings text that the PC is kept awake for the test.

#### Two viewers: paths that decode on the processor are counted as card work, and a full card cap refuses a film the processor could take

*Severity: medium.*

**Evidence.** READ: H.4 Caps, "`max(1, cores/4)` stays the rule for software conversions. Card conversions: at most 2 at once ... `Busy` in the planner counts conversions on the same kind of path." Paths (a) and (c1) decode, size and tone-map on the processor (H.2: "Decoding and the whole filter chain are the software row's"), and for a 4K HDR film that is the measured cost (decoding alone 3.7x, with tonemapx 1.67x to 2.2x on four processors, M4.md section 8). So an (a) play beside a software play, or two (a) plays, are each planned as if the processor were free: both run near half speed, under the 1.2x bar, and stall, where the old cap would have told the second viewer plainly that the PC is busy. Second gap: when two card conversions run, the planner still finds the card fast enough (speed divided by Busy+1), and what happens at the cap is not said; M4.md section 3 says `Open` refuses beyond the cap. If that rule is kept, the third viewer is refused although the processor is free.

**Suggested.** Count what a path costs where it costs it: (a) and (c1) take a place under the processor's cap and in the processor's Busy for every film they decode in software; only (b) and (c2) count as card work. A path at its cap is "not available" to the planner, which goes down the order to software before anything is refused. Add both to the planner's property test.

#### After a failed card start the processor converts at the size chosen for the card

*Severity: medium.*

**Evidence.** READ: H.4, "the session's spec becomes the software path (straight there, not down the order: one extra start, not two)". The rung was picked with the card's speeds (H.1.3; M4.md section 2: first rung whose measured speed clears the bar). For a 4K HDR film that is rung 1 (1080p) on the card; the software table for the same film may say 720p or "cannot convert fast enough" (1.67x at 1080p on four processors, less for a 60 Mbit/s copy, M4.md section 8). The relaunch also bypasses the software cap. The viewer then gets a start, a stall and a step down, and the page shows a plan (size, what was lost) that is no longer the one running. Nothing was served yet, so nothing forces the old size to be kept.

**Suggested.** Make the relaunch a new plan under the software path's own speeds, cap and rungs, and hand the page the new plan in the same answer. If the software plan is "cannot convert fast enough", say that instead of starting.

#### A driver changed while the program runs is not noticed, and what happens to a path that was on is not said

*Severity: medium.*

**Evidence.** READ: H.6, the driver version is "read from the registry at every program start". A tray program on a laptop sleeps and wakes for weeks without restarting, and graphics drivers install without a restart (from memory). After an overnight driver update the program holds the old version: the path stays on, the speed table stays keyed to the old driver, and every play is on an untested driver, against H.6's own "Never a guess on a real play". The design also does not say whether a path that was on stays on between a noticed change and the next passed test: the owner's sentence ("The graphics driver has changed since the last test. The test will run by itself...") leaves it open. If it goes off, then with "when ffmpeg or the app changes" every automatic app update, also one that touches only the page, puts 4K HDR films back on the processor until the laptop is next idle on mains, and the owner sees the same film at 1080p one evening and 720p the next. A play in progress across a driver install dies with unseen words (see the finding on unseen wordings).

**Suggested.** Read the driver versions before each card start and after each detected gap (a registry read, said not to wake the card). Key verdicts and speeds by converter version, driver version and a version number of the row table, not by the app's version. Say which it is: "off until re-tested" (and tell the owner in the notice) or "on, on trial".

#### Failures in real plays reach the designer only as log lines; the state of the paths is not in the bundle

*Severity: medium.*

**Evidence.** READ: the bundle gains `hardware-report*.json` and `power.json` (H.5, H.9). A card failure in a real play calls `OnPathFailed(path, class, kind of film)`; nothing says it is kept for the bundle. What exists today: the run's last 4,096 bytes go to the log as "the converter stopped: ..." with folders removed (hls.go lines 745-754 and 1046-1087; routes_play.go `pathless`), and real rows run at `-loglevel error`, so the lines naming the driver, the card and what the encoder really did are never printed. For "passed last week, fails tonight" a designer who cannot touch the PC needs what the bundle would not hold: which path and class, driver versions then and at the last pass, mains or battery, seconds since the last wake, how many conversions ran, whether chunks had been served, what the path's state is now and when it last changed, and the driver's own lines. The cleaning rules of H.7 are written for the test only; real output names the film's folder, which today's `said` and `pathless` remove, but the user-name and computer-name rules are not applied there.

**Suggested.** Add `paths.json` to the bundle: each path's state, since when and why, the last passed report's id, and the last 50 failures with time, path, class, kind of film (never a title), driver versions, power source, seconds since the last gap, conversions running, served or not, and the cleaned first and last lines. On a card failure run the path's start row once at `level+verbose` straight afterwards and keep its lines. Use one cleaning function for test and play, with the library folders among the private texts.

#### What H.5 says a report tells the designers does not fit the test's own time budget

*Severity: medium.*

**Evidence.** READ: H.5 lists as report contents "two 4K conversions at once; which formats the card decodes; size and likeness at `-cq` 20, 23 and 26"; H.2 lets VP9 and AV1 onto (b) only "if the report's decoding table says"; H.3 names `nvidia-full/gaps/jump` as the probe that settles the picture after a jump; H.4 holds the cap at 2 "until the report's 'two at once' figure". Probe section 6 puts all of these among the extras, run "only if the time left exceeds its limits", inside a budget of 4 min 30 s (cap 4 min 50 s) of which the essentials are expected to take "about three and a half minutes". MY ARITHMETIC from the specification's limits (an estimate; nothing ran on a card): about one minute is left; `g146` needs 45 s of limits, `p1` 65 s, each decoding sample 30 s, each quality run 45 s plus seven comparisons at 30 s, `hlg` and `gaps` over a minute each, `pair` last. On a laptop where both vendors work the first report will most likely end inside the decoding table, with quality, HLG, gaps and the pair "not tried: out of time". Every missing item is another round through a person who is not a developer: press, download, attach.

**Suggested.** Give the button a long form the owner agrees to ("about ten minutes; you can keep using the PC") that runs every extra, or let a report continue where it stopped at the next press, in the same file. Move `pair` and `gaps` to the essentials of the path they decide. State the expected time per group in the report so the budget can be tuned from the first one.

#### The cost of waking the card cannot be read from the report

*Severity: medium.*

**Evidence.** H.1.3: "the test measures start-up time cold and warm and the order is revisited with numbers"; probe section 6: `nvidia/start`, then `nvidia/start-again` "(the difference is the cost of waking the card)". But "Looking at the PC" runs before the starts and includes `nvidia-smi` (section 9). REPORTED for Linux only: users on NVIDIA's forum say nvidia-smi itself wakes a sleeping card ("Nvidia-smi keeps gpu awake with on-demand prime profile", forums.developer.nvidia.com, seen as a search summary); for Windows I could not find out. If it wakes the card there too, the first start is always warm and the report will show "waking costs nothing". Beyond that the report cannot say whether the card was asleep: after the owner pressed a button in a browser, with an outside screen on a port wired to the NVIDIA card, or in a laptop's "NVIDIA only" mode it never sleeps. The quiet run comes nearest to a cold card and is the one the owner did not ask for. The start-up figure also has to supply the deadline asked for in the first finding.

**Suggested.** Run `nvidia-smi` after the starts and again at the end. Record seconds since this program last used the card and whether the NVIDIA adapter has a screen attached (Windows can list that per adapter; from memory). Report `startupMs.first` as "state before unknown" unless both say the card had cause to sleep. Later, in stage two, record the time to first fragment of every real card play with the seconds since the last one: that is the real cold figure.

#### A card that dies mid-film on the iPhone: the way back is not tested where it is hardest

*Severity: medium.*

**Evidence.** READ: H.4, "A card death after chunks were served answers `ErrEncoderDied` (HTTP 410); the page replays at the same position"; acceptance test 3 (ii) checks the 410 and that "the next plan is software", nothing about the page. On Apple devices the page hands the chunk list to the browser's own player (web/src/caps.ts lines 113-135; App.tsx line 934), so the page never sees the 410; it sees a stall or an error and must ask the server, then load a new stream. INFERRED, not run: whether an iPhone lets a page start a new stream without a tap, inside the phone's own full-screen player, in picture-in-picture, or while sending to a TV with the screen locked, where pages are held back. M4 decision 10 avoided exactly this ("The stream is never swapped when the phone enters its own player"). With the processor alone a mid-film encoder death hardly happens, so this is a failure the card path adds. Also not said: what the owner's switch "Use the graphics card to convert films" does to plays in progress. An owner who switches it off because the laptop is loud expects quiet now.

**Suggested.** A browser test in Safari's engine at GitHub: a stand-in dies after two chunks and the film must go on at the same place without a click. An owner's checklist step on the iPhone in its own player and in picture-in-picture, using the switch as the trigger: say that switching it off ends running card conversions (each replays on the processor). Where the phone cannot replay by itself, the page must say "Tap to go on" instead of stopping silently.

#### Sentences for the owner that are not true in every case they are shown

*Severity: medium.*

**Evidence.** READ (owner_words against H.4, H.6 and the probe's classes). (1) "The graphics card stopped working while a film was playing ... stays switched off until the test passes again. Updating the graphics driver may help." is one sentence for nine classes: untrue for busy (not off), no card just now (until program start), bad state (quiet start run after 10 minutes), and the driver advice is wrong for a game, a full session table, battery or a damaged film. (2) "The driver for the Intel graphics is too old" has no class behind it: `intel-runtime` ("Error creating a MFX session") also arises in a remote-desktop session or with a disabled adapter (Intel research). (3) "on battery the graphics card is held back" states as fact what the research calls "my expectation, not sourced". (4) "about 14 times faster than playing speed" beside "about 4 times playing speed": two forms for the same figure, and the first reads as 15 times. (5) "Your NVIDIA graphics card can convert films" in stage one is Judge's provisional verdict before any phone has played a chunk. (6) "Step 3 of 7" counts stages that are skipped on a PC without Intel graphics or where the card does not start. (7) "version 522.25 or newer is needed": RAN `strings` on the pinned Linux build: it holds "520.56.06" and the sentence "The minimum required Nvidia driver for nvenc is %s or newer", not 522.25; H.4's "[ran: the string is in the binary]" is true of the Linux number only, the Windows number is from the headers' README. (8) "Try the graphics card on this film" and "Yes, they look right" can be pressed by any signed-in viewer and change a setting of the PC.

**Suggested.** One sentence per class, each naming its own remedy (close the game; another program is using the card; plug in; nothing to do, it will be tried again by itself). For Intel say "could not be started" and offer the driver as one possibility. "May be held back". One form for speeds. "Passed this test" in stage one. Number only the stages that will run. Quote the driver number from ffmpeg's own line on the PC. Make the Dolby Vision question the owner's.

#### Staying awake: what the page does not say

*Severity: medium.*

**Evidence.** The design is honest on the main points: it says the lid, the power button and Sleep cannot be prevented, that battery ends the request on the newer sleep kind, never changes a Windows setting, never asks for the screen, and gives the owner tests and a fallback. Gaps, READ against H.9 and the research: (1) "Two things can still stop a film" is a closed count; H.9 itself lists more (power button, Sleep in the Start menu, hibernation at low battery, a maker's policy), a restart for Windows updates is not considered anywhere (whether a request holds one off: I could not find out), and the locked PC is unknown. (2) "Running on battery ... Windows puts the laptop to sleep after a few minutes, even in the middle of a film" is certain only for the newer sleep kind; on the older kind Microsoft's text has the request honoured. `power.json` reads which kind it is, so the page can choose. (3) The fallback "set 'Make my device sleep after' to 'Never'" means the laptop never sleeps again while plugged in, film or no film, lid steps or not; the text does not say so. (4) Lid set to "Do nothing" for mains, lid shut, then the plug pulled or a power cut: the research could not confirm that Windows applies the battery lid action to a lid already shut. On a laptop of the older kind the request would then keep it converting, shut, on battery. Not in the words or the checklist. (5) The quiet test holds its own request ("Local Media: testing the graphics card") that the setting's text does not mention; one handle "counting 0 or 1" cannot carry two reasons. (6) The button test is not said to hold a request: pressed from the phone with the laptop idle for four minutes, the laptop sleeps mid-test and the owner is told to run it again. (7) The "fifteen minutes" sentence is untrue with the merged Watching (separate finding).

**Suggested.** "The two most common ..." plus one line naming the rest. Choose the battery sentence by sleep kind, or say "may". Add to the fallback: "The PC will then stay on whenever it is plugged in." Add a checklist step: lid shut on mains, pull the plug, see that it sleeps within ten minutes; and release the request at once when battery and a shut lid are seen together (the lid state arrives with a power-setting notification; from memory). Hold a request for every test, with its own handle, and say so in the setting.

#### Cleaning rules: small leaks and losses when realistic text is run through them

*Severity: low.*

**Evidence.** RAN (<scratch>/hw.O6hs74/py/scrub.py, my literal reading of probe section 12 on made-up lines). (1) Rule 6 removes " @ 0x" plus hex; Windows builds print the address without "0x" (`[h264_nvenc @ 000001e0d0d1e400]`; from memory of many Windows logs), so on the owner's PC addresses stay: not private, but two runs' words are not comparable and equal neighbours do not collapse, the two stated purposes. (2) "`[path]` plus its last part" keeps the last part even when it is the private one: `Could not create directory C:\Users\MARIEN~1` came out as `[path]MARIEN~1`. Windows hands out such short forms in TEMP for long account names (from memory); the short form holds six letters of the name and is no whole-word match. (3) Rule 7 (bytes that are not UTF-8 become "?") comes after the name rules, so a name printed in the console's code page by a Windows tool is never matched. (4) The address rule turns a four-part version of short numbers (`9.18.13.697`) into `[address]`; today's NVIDIA and Intel shapes (`32.0.15.6094`, `31.0.101.5590`) pass. (5) "Unix paths of two or more parts" is open to reading: taken as any slash-joined words it would eat `bt2020nc/bt2020/smpte2084` and `24000/1001`; taken as starting with a slash it does not. (6) Go's own error for a program that cannot be started quotes the full program path; the specification's cleaning names "every line from a process", not errors of the package. (7) `hls.ProbePicture(ctx, FFprobe, filepath.Join(WorkDir, NAME))` passes an absolute path as an argument, against the package's own rule and argument test; it is not stored, so nothing leaks.

**Suggested.** Remove ` @ ` followed by 8 to 16 hex digits, with or without 0x. Keep a path's last part only when it has a dot-extension from a fixed list; otherwise `[path]`. Decode before matching. Clean version strings by their own pattern, before the address rule. Say "starting with /". Clean the package's own error texts. Probe the clips by relative name with the work folder as the process folder.

#### Looking at the PC: entries that may not be there, a card without a driver, and names beyond card and processor

*Severity: low.*

**Evidence.** READ: probe section 9 takes adapters from the display class key's subkeys `0000` to `0015` and the vendor from `MatchingDeviceId`. FROM MEMORY, not run (the design also marks these reads as from memory): that key keeps entries for adapters that are no longer present (earlier installs, a dock, a second card), so the report can show Intel graphics or a driver version the PC does not have now, and "driver changed" can fire or stay silent wrongly. A card whose driver is missing shows there as Microsoft's basic adapter with a generic id, so vendor reads "other", Judge says `no-card-found`, and the owner reads "No graphics card that can help was found in this PC" on a laptop with an RTX 3060, where the right sentence is "its driver is missing". The same list would carry the names of virtual adapters that remote-desktop and screen-sharing programs install: a detail about the PC beyond "card and processor model names, driver versions" (H.7).

**Suggested.** Take the adapters Windows has active in this boot (the volatile video device map points at them; verify on GitHub's Windows machine) and the hardware's own vendor id rather than the driver's. Keep names only for NVIDIA, Intel and AMD; write "other" for the rest. Add a class "card present, no driver" to Judge.

#### Speeds come from four seconds on a cool laptop

*Severity: low.*

**Evidence.** READ: probe section 5, a speed is frames in a 4.0 s window, opened at the first progress line; these figures fill the speed table (H.6). A laptop runs above its lasting power for the first tens of seconds and slows as it warms, more with the lid shut (general knowledge; no figure for this model), and a film lasts two hours. The margin of 2.0 covers some of it; "real plays only lower it" covers the rest at the price of a stall. Second, unknown: on Intel chips with two kinds of cores (12th generation on; an RTX 3060 laptop may have one) users report Windows 11 moving work without a visible window to the slow cores (elevenforum.com, "Forcing performance mode for background app on Intel P/E-core CPU", seen as a search summary; Microsoft documents `SetProcessInformation` with `ProcessPowerThrottling` as the way for a process to opt out). ffmpeg here is a hidden child of a tray program. Whether that applies, and whether it differs between a test started from the browser and a play at night with the screen off, I could not find out. The report records `processor` (the model tells the core kinds) but not Windows' power mode. This concerns the software path and path (a) as much as the card.

**Suggested.** Make the last speed run of each path 20 to 30 seconds and report the first and the last 4 seconds separately; record Windows' power mode and the counts of fast and slow cores; add "screen off" and "lid shut" speed lines from real plays to the bundle. If the owner's report shows the split, try the opt-out call on the child process and measure again.

#### Smaller points: speed runs that never end by themselves, and the hardware classes' own retry rule

*Severity: low.*

**Evidence.** (1) RAN on the pinned build (<scratch>/hw.O6hs74, a tiny software clip): a speed run as specified (`-stream_loop -1 ... -progress pipe:1 ... -f null -`) went on after the reader of its pipe left at 0.5 s and ran until killed at 6 s, printing nothing. Only the parent ends it. On Windows the helper tie merged in e473e3f (cmd/localmedia/helpers_windows.go) ends ffmpeg children with the program, after a first look that can be 2 s late; elsewhere nothing does (helpers_other.go), and hwprobe's tests run on Linux and Mac at GitHub. The updater and the quiet test wait for the same idle moment. (2) H.4 says a "bad state" path is switched back on by "one quiet start run", H.6 says "'Tried again' always means the test for that path runs and must pass": two rules for one event.

**Suggested.** Give speed runs an end of their own (`-t 30` on the output) and say in the specification that the test relies on the helper tie. Have the updater wait for a running test. Choose one retry rule for bad state and write it in both places.

**What held up**

- A driver that is a year old: tried to make it fail and could not. The build needs NVIDIA's SDK 12.0, which is driver 522.25 on Windows (October 2022; research from the headers' README, and the failure sentence is in the pinned binary: ran `strings`). A driver from late 2025 passes. A never-updated 2021 driver fails at the first start run with words the class table has, and the owner gets the version found, the version needed and two ways to update, then "run this test again".
- Every failure that ends a card run with words ends in the processor for that play: I went through each row of H.4 and found no class whose "this play" column is anything but software. One path per play matches the fact that headers differ between encoders, and falling straight to software costs one extra start, not two.
- A card that cannot decode a film cannot quietly drag it down: with a card filter first in the chain, ffmpeg's silent fall-back to processor decoding (single-threaded under `-threads 1`) ends the run instead of crawling (research: the filter failure ran here, exit 218), and the run is relaunched in software.
- The hardware test and a film: a play cancels the test at once, the button is refused while someone watches, and stage one changes nothing about how films are played. Apart from a child that cannot be ended and a test started under someone using the laptop for something else (both in the findings), I found no way for the test to make a film unplayable.
- Privacy by construction holds for what the test starts: arguments are whole numbers and fixed words, clips have fixed names, the process folder is the work folder. Ran a literal reading of the cleaning rules over made-up lines of the kinds ffmpeg, Go and Windows print: an account name with a space in a plain path, in a Go-quoted path with doubled backslashes, and a computer name in a network path with spaces all came out as placeholders; the card's name line and Intel's "Using device 8086:..." line passed untouched, as wanted; today's NVIDIA and Intel driver version shapes are not taken for addresses.
- No text ffmpeg or the drivers print in these runs can name a film or the library: the package is given two programs and a work folder, and `hls.PictureFacts` (read: internal/hls/facts.go) holds numbers and format words only, no path.
- Staying awake is honest on its three named limits. Lid: said plainly that no program can prevent it, with steps that change only the plugged-in box and the warning about a bag. Battery: said, with a banner while someone watches. The newer sleep kind: the five-minute rule on battery is from Microsoft's own page, mains is tested by the owner's 25-minute step, and the fallback is a setting the owner changes, not the app. It asks for neither the screen nor away mode and never edits a power plan.
- The power-request binding as outlined: the REASON_CONTEXT layout (two 32-bit fields, a pointer, two more pointer-sized words) comes to the 32 bytes the research found in the headers; `Find()` before `Call()` and judging by the first return value match what the research read in golang.org/x/sys.
- If the i7 has no Intel graphics, or the laptop is set to NVIDIA only: I looked for anything else that changes and found only what the design says (both Intel paths do not exist, asked again at every test), plus one consequence it does not state: the card then never sleeps, so there is no waking cost to measure, and games and films always share it.
- Telling failures apart by words and never by exit code survives everything I tried: the same build ended with different numbers for different failures here (research), a crash inside a driver library on Windows would show as a large exit number with no words, and the report keeps the number beside the class, so a designer can still see that it was a crash.
- Real-play failures are not lost entirely today: the manager already logs a stopped run's last words with the film's folder, the data folder and the program folder removed (hls.go lines 745-754, routes_play.go `pathless`), and the log is in the bundle. What is missing is structure and the driver's own lines, not the bare fact.

