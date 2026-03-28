from datetime import datetime
from zoneinfo import ZoneInfo
from streamcoreai_plugin import StreamCoreAIPlugin

plugin = StreamCoreAIPlugin()


@plugin.on_execute
def handle(params):
    tz_name = params.get("timezone", "UTC")
    try:
        tz = ZoneInfo(tz_name)
    except Exception:
        return f"Unknown timezone: {tz_name}"

    now = datetime.now(tz)
    return (
        f"The current time in {tz_name} is {now.strftime('%A, %B %d, %Y at %I:%M %p')}."
    )


plugin.run()
