import pandas as pd
import matplotlib.pyplot as plt
import sys

file_path = sys.argv[1]

df = pd.read_csv(file_path)

# Convert 'time' column to datetime if it exists
if "time" in df.columns:
    df["time"] = pd.to_datetime(df["time"], errors="coerce")

# Extract rows where "name" is "cpu_self"
df_cpu_self = df[df["name"] == "cpu_self"].copy()

# Reshape cumulative CPU usage time (seconds) using pivot
# index: time, columns: process name (label), values: CPU usage time (value)
df_pivot = df_cpu_self.pivot_table(
    index="time", columns="label", values="value", aggfunc="last"
).ffill()
df_pivot = df_pivot.sort_index()

# Calculate difference from previous value
# (since it's cumulative, the difference represents the increase in usage time)
df_diff = df_pivot.diff(periods=10)

# Calculate time difference in seconds
time_diff = df_pivot.index.to_series().diff(periods=10).dt.total_seconds()

# Calculate CPU usage rate per second for each process
# (difference) / (elapsed seconds)
df_rate = df_diff.div(time_diff, axis=0)

# Remove first row which is NaN due to diff operation
df_rate = df_rate.dropna()

# Plot graph
ax = df_rate.plot(figsize=(12, 6))
ax.set_title("Process CPU Usage Rate (sec/sec)")
ax.set_xlabel("Time")
ax.set_ylabel("CPU usage change per second (sec/sec)")
ax.legend(title="Process", loc="upper left")

plt.tight_layout()
plt.show()
