import asyncio
from viam.module.module import Module
try:
    from models.firebase_push import FirebasePush
except ModuleNotFoundError:
    # when running as local module with run.sh
    from .models.firebase_push import FirebasePush


if __name__ == '__main__':
    asyncio.run(Module.run_from_registry())
