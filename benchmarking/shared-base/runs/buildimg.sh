#!/bin/bash
set -e
H=/home/fkautz
R=$H/gvisor/bazel-bin/runsc/runsc_/runsc
rm -f $H/buildimg.done
rm -rf $H/adkimg $H/adkbundle
mkdir -p $H/adkimg $H/adkbundle/rootfs
cp $H/agent.py $H/adkimg/
cat > $H/adkimg/Dockerfile <<DOCKER
FROM python:3.12-slim
RUN pip install --no-cache-dir google-adk
COPY agent.py /agent.py
DOCKER
docker build -t adkagent $H/adkimg >/dev/null 2>&1
cid=$(docker create adkagent)
docker export $cid | tar -x -C $H/adkbundle/rootfs
docker rm $cid >/dev/null
cd $H/adkbundle
$R spec
python3 - <<PY
import json
c=json.load(open("config.json"))
c["process"]["args"]=["python3","/agent.py"]
c["process"]["terminal"]=False
c["process"]["cwd"]="/"
env=[e for e in c["process"]["env"] if not e.startswith("PAD_MB=")]
env+=["PYTHONUNBUFFERED=1","PAD_MB=0"]
c["process"]["env"]=env
json.dump(c,open("config.json","w"),indent=2)
PY
echo "python in rootfs: $(ls $H/adkbundle/rootfs/usr/local/bin/python3)" > $H/buildimg.done
echo "rootfs size: $(du -sh $H/adkbundle/rootfs | cut -f1)" >> $H/buildimg.done
