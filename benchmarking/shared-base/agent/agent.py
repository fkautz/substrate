import asyncio, os, time
from google.adk.models import BaseLlm, LlmResponse
from google.adk.agents import LlmAgent
from google.adk.runners import InMemoryRunner
from google.genai import types

class MockLlm(BaseLlm):
    model: str = "mock-llm"
    async def generate_content_async(self, llm_request, stream=False):
        txt = ""
        try:
            for c in reversed(list(llm_request.contents)):
                role = getattr(c, "role", None)
                if role in (None, "user") and getattr(c, "parts", None):
                    for p in reversed(c.parts):
                        if getattr(p, "text", None):
                            txt = p.text; break
                    if txt: break
        except Exception as e:
            txt = "ERR:" + type(e).__name__
        yield LlmResponse(content=types.Content(role="model", parts=[types.Part(text="echo:" + txt)]))

APP, USER, SESS = "densitytest", "u1", "s1"
agent = LlmAgent(name="mock_agent", model=MockLlm(), instruction="Echo agent.")
runner = InMemoryRunner(agent=agent, app_name=APP)
asyncio.run(runner.session_service.create_session(app_name=APP, user_id=USER, session_id=SESS))

def rss_mb():
    for ln in open("/proc/self/status"):
        if ln.startswith("VmRSS:"): return int(ln.split()[1]) // 1024
    return 0

state_sum, tick = 0, 0
while True:
    tick += 1
    q = f"ping-{tick}"
    t0 = time.perf_counter()
    events = list(runner.run(user_id=USER, session_id=SESS,
                 new_message=types.Content(role="user", parts=[types.Part(text=q)])))
    dur_ms = (time.perf_counter() - t0) * 1000.0
    got = ""
    for e in events:
        c = getattr(e, "content", None)
        if c and getattr(c, "parts", None) and c.parts[-1].text:
            got = c.parts[-1].text
    ok = got.endswith(q)
    state_sum = (state_sum + tick) & 0xffffffff
    print(f"tick={tick} dur_ms={dur_ms:.1f} resp={'ok' if ok else 'BAD('+got+')'} state_sum={state_sum} rss={rss_mb()}MiB", flush=True)
    time.sleep(0.25)
