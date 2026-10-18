FROM ollama/ollama:latest

# Models are baked into the image at build time, so containers start ready (and work offline).
# Change the list to add models; Docker caches this layer, so rebuilds only re-download on change.
ARG MODELS="qwen3:4b-instruct"
RUN ollama serve & \
    until ollama list >/dev/null 2>&1; do sleep 1; done && \
    for m in $MODELS; do ollama pull "$m"; done
