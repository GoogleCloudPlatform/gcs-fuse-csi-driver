#include <iostream>

extern "C" {
    void mock_encrypt() {
        std::cout << "MOCK CRYPTO: Encrypting using C++ standard library (cout)..." << std::endl;
    }
}
