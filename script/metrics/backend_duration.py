import pandas as pd
import matplotlib.pyplot as plt
import sys
from matplotlib.dates import DateFormatter

file_path = sys.argv[1]

df = pd.read_csv(file_path)

# Parse the "time" column if it contains datetime values
if "time" in df.columns:
    df["time"] = pd.to_datetime(df["time"], errors="coerce")

# Extract only rows where the "name" column is "backend_duration"
df_duration = df[df["name"] == "backend_duration"].copy()

# Extract rows with specific label values (get, put)
valid_labels = ["get", "put", "close"]
df_duration = df_duration[df_duration["label"].isin(valid_labels)]

# Convert nanosecond values to seconds
df_duration["value"] = df_duration["value"] / 1_000_000_000

# Set time as index if available
if "time" in df_duration.columns:
    df_duration.set_index("time", inplace=True)

# Create pivot table with duration data
if df_duration.empty:
    print("Error: Input DataFrame is empty")
    sys.exit(1)

df_pivot = df_duration.pivot_table(
    index=df_duration.index, columns="label", values="value", aggfunc="mean"
).ffill()

# Create a line graph
fig, ax = plt.subplots(figsize=(12, 6))
df_pivot.plot(kind="line", ax=ax)

# Format x-axis
ax.xaxis.set_major_formatter(DateFormatter("%H:%M:%S"))
plt.xticks(rotation=45)

# Set axis labels and title
ax.set_xlabel("Time")
ax.set_ylabel("Duration (seconds)")
ax.set_title("Backend Operation Duration")
ax.legend(loc="upper left")

# Adjust layout to prevent label cutoff
plt.tight_layout()
plt.show()
