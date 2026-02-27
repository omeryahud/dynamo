sudo apt install -y build-essential libhwloc-dev libudev-dev pkg-config libclang-dev protobuf-compiler python3-dev cmake

curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
source $HOME/.cargo/env

curl -LsSf https://astral.sh/uv/install.sh | sh

uv venv dynamo
source dynamo/bin/activate

uv pip install pip maturin

cd lib/bindings/python
maturin develop --uv

cd ../../../
uv pip install -e lib/gpu_memory_service

uv pip install -e .

