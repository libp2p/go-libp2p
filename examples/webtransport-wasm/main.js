const go = new Go();

const inst = await WebAssembly.instantiateStreaming(fetch("main.wasm"), go.importObject);
go.run(inst);