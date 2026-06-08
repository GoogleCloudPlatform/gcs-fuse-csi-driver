all: libmockcrypto.so mock-client

libmockcrypto.so: mock_crypto.cpp
	g++ -shared -fPIC -o libmockcrypto.so mock_crypto.cpp

mock-client: main.rs libmockcrypto.so
	rustc -L . -l mockcrypto main.rs -o mock-client

static: libmockcrypto.a mock-client-static

libmockcrypto.a: mock_crypto.cpp
	musl-gcc -c -fPIC -x c++ mock_crypto.cpp -o mock_crypto.o
	ar rcs libmockcrypto.a mock_crypto.o

mock-client-static: main.rs libmockcrypto.a
	rustc --target x86_64-unknown-linux-musl -L . -l static=mockcrypto main.rs -o mock-client-static

clean:
	rm -f libmockcrypto.so mock-client libmockcrypto.a mock_crypto.o mock-client-static
