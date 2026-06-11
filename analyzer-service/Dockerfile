FROM python:3.11-slim AS base
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg wget ca-certificates \
    libyaml-dev libfftw3-dev libtag1-dev libchromaprint-dev libsamplerate0-dev && \
    rm -rf /var/lib/apt/lists/*

# AMD64 Build Stage
FROM base AS build-amd64
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# ARM64 Build Stage
FROM base AS build-arm64
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential git libyaml-dev libfftw3-dev libavcodec-dev \
    libavformat-dev libavutil-dev libswresample-dev libsamplerate0-dev \
    libtag1-dev libchromaprint-dev libeigen3-dev pkg-config python3-dev && \
    rm -rf /var/lib/apt/lists/*
COPY requirements.txt .
RUN pip install --no-cache-dir --upgrade pip && \
    pip install --no-cache-dir tensorflow==2.15.1 numpy==1.26.4 && \
    grep -vE "essentia-tensorflow|numpy" requirements.txt > req_arm.txt && \
    pip install --no-cache-dir -r req_arm.txt && \
    git clone --depth 1 https://github.com/MTG/essentia.git /tmp/essentia && \
    cd /tmp/essentia && \
    find /usr/local/lib/python3.11/site-packages -name "*.so*" -exec cp -P {} /usr/local/lib/ \; && \
    [ -f /usr/local/lib/libtensorflow_framework.so.2 ] && ln -sf /usr/local/lib/libtensorflow_framework.so.2 /usr/local/lib/libtensorflow_framework.so || true && \
    [ -f /usr/local/lib/libtensorflow_cc.so.2 ] && ln -sf /usr/local/lib/libtensorflow_cc.so.2 /usr/local/lib/libtensorflow_cc.so || true && \
    [ -f /usr/local/lib/_pywrap_tensorflow_internal.so ] && ln -sf /usr/local/lib/_pywrap_tensorflow_internal.so /usr/local/lib/libpywrap_tensorflow_internal.so || true && \
    ldconfig && \
    TF_LIBS="-Wl,--no-as-needed -lpywrap_tensorflow_internal -ltensorflow_framework" && \
    [ -f /usr/local/lib/libtensorflow_cc.so ] && TF_LIBS="$TF_LIBS -ltensorflow_cc" || true && \
    mkdir -p /usr/local/lib/pkgconfig && \
    echo "prefix=/usr/local" > /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "exec_prefix=\${prefix}" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "libdir=\${exec_prefix}/lib" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "includedir=\${prefix}/lib/python3.11/site-packages/tensorflow/include" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "Name: TensorFlow" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "Description: TensorFlow library" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "Version: 2.15.0" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "Cflags: -I\${includedir}" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    echo "Libs: -L\${libdir} $TF_LIBS" >> /usr/local/lib/pkgconfig/tensorflow.pc && \
    export PKG_CONFIG_PATH=/usr/local/lib/pkgconfig && \
    python3 waf configure --with-python --with-tensorflow && \
    LDFLAGS="-L/usr/local/lib $TF_LIBS" python3 waf -j 1 && \
    python3 waf install && \
    cd /app && rm -rf /tmp/essentia

# Select the appropriate build stage based on TARGETARCH
ARG TARGETARCH
FROM build-${TARGETARCH} AS builder

# Final stage
FROM base
COPY --from=builder /usr/local/lib/python3.11/site-packages /usr/local/lib/python3.11/site-packages
COPY --from=builder /usr/local/lib /usr/local/lib
COPY --from=builder /usr/local/bin /usr/local/bin
RUN ldconfig && \
    [ -f /usr/local/lib/libtensorflow_framework.so.2 ] && ln -sf /usr/local/lib/libtensorflow_framework.so.2 /usr/local/lib/libtensorflow_framework.so || true && \
    [ -f /usr/local/lib/libtensorflow_cc.so.2 ] && ln -sf /usr/local/lib/libtensorflow_cc.so.2 /usr/local/lib/libtensorflow_cc.so || true && \
    [ -f /usr/local/lib/_pywrap_tensorflow_internal.so ] && ln -sf /usr/local/lib/_pywrap_tensorflow_internal.so /usr/local/lib/libpywrap_tensorflow_internal.so || true && \
    ldconfig

# Download Essentia TF mood models
RUN mkdir -p /app/models && \
    wget -q -P /app/models https://essentia.upf.edu/models/feature-extractors/discogs-effnet/discogs-effnet-bs64-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_happy/mood_happy-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_sad/mood_sad-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_relaxed/mood_relaxed-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_aggressive/mood_aggressive-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/mood_party/mood_party-discogs-effnet-1.pb && \
    wget -q -P /app/models https://essentia.upf.edu/models/classification-heads/danceability/danceability-discogs-effnet-1.pb

COPY app.py /app/app.py
WORKDIR /app
EXPOSE 8000
CMD ["sh", "-c", "uvicorn app:app --host 0.0.0.0 --port 8000 2>&1 | grep -v 'No network created'"]
