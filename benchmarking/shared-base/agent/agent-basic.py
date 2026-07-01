import asyncio, os, time, sys
from google.adk.models import BaseLlm, LlmResponse
from google.adk.agents import LlmAgent
from google.adk.runners import InMemoryRunner
from google.genai import types

class MockLlm(BaseLlm):
    model: str = "mock-llm"
    async def generate_content_async(self, llm_request, stream=False):
        # canned response; NO network to any LLM endpoint
        yield LlmResponse(content=types.Content(role="model",
                          parts=[types.Part(text="ack")]))

APP, USER, SESS = "densitytest", "u1", "s1"
agent = LlmAgent(name="mock_agent", model=MockLlm(), instruction="Test agent.")
runner = InMemoryRunner(agent=agent, app_name=APP)
asyncio.run(runner.session_service.create_session(app_name=APP, user_id=USER, session_id=SESS))

pad_mb = int(os.environ.get("PAD_MB", "0"))
pad = None
if pad_mb:
    pad = bytearray(pad_mb * 1024 * 1024)
    for i in range(0, len(pad), 4096):
        pad[i] = 1  # touch -> resident

def rss_mb():
    for ln in open("/proc/self/status"):
        if ln.startswith("VmRSS:"):
            return int(ln.split()[1]) // 1024
    return 0

print(f"ready adk-agent warm rss={rss_mb()}MiB pad={pad_mb}MiB pid={os.getpid()}", flush=True)
tick = 0
while True:
    tick += 1
    events = list(runner.run(user_id=USER, session_id=SESS,
                  new_message=types.Content(role="user", parts=[types.Part(text=f"ping {tick}")])))
    ok = any(getattr(e, "content", None) and e.content.parts for e in events)
    print(f"tick={tick} events={len(events)} resp={'ok' if ok else 'none'} rss={rss_mb()}MiB", flush=True)
    time.sleep(2)
