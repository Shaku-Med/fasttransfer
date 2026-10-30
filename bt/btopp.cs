// Scans for nearby Bluetooth devices when asked, then sends chosen files with OBEX PUT.
// It does not advertise this PC. Pairing runs only for the device the user picked.
using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using System.Threading;
using Windows.Devices.Bluetooth;
using Windows.Devices.Bluetooth.Rfcomm;
using Windows.Devices.Enumeration;
using Windows.Devices.Radios;
using Windows.Foundation;
using Windows.Networking.Sockets;
using Windows.Storage.Streams;

static class Program {
    const int ReadTimeoutMs = 60000;

    [STAThread]
    static int Main(string[] args) {
        try {
            if (args.Length > 0 && args[0] == "check") return Check();
            if (args.Length > 0 && args[0] == "scan") return Scan();
            if (args.Length > 0 && args[0] == "list") return List();
            if (args.Length > 0 && args[0] == "send") return Send(args);
            return 2;
        } catch (Exception ex) {
            string msg = "The phone refused the file.";
            if (args.Length > 0 && args[0] == "list") msg = "No paired phone was found.";
            if (IsRadioOff(ex)) msg = "Bluetooth is off.";
            if (args.Length > 0 && args[0] == "list") ListMessage(msg);
            else Error(msg);
            return 1;
        }
    }

    static int List() {
        string off = RadioProblem();
        if (off != null) {
            ListMessage(off);
            return 0;
        }
        string selector = RfcommDeviceService.GetDeviceSelector(RfcommServiceId.ObexObjectPush);
        DeviceInformationCollection infos = Wait(DeviceInformation.FindAllAsync(selector), 20000);
        var sb = new StringBuilder();
        sb.Append("{\"ok\":true,\"message\":\"\",\"devices\":[");
        bool any = false;
        if (infos != null) {
            int count = infos.Count;
            for (int i = 0; i < count; i++) {
                DeviceInformation info = infos[i];
                if (info == null || string.IsNullOrEmpty(info.Id)) continue;
                if (!IsPaired(info)) continue;
                string name = info.Name;
                if (string.IsNullOrWhiteSpace(name)) name = "Phone";
                if (any) sb.Append(',');
                any = true;
                sb.Append("{\"id\":\"").Append(JsonEscape(info.Id)).Append("\",\"name\":\"").Append(JsonEscape(name.Trim())).Append("\"}");
            }
        }
        sb.Append("]}");
        if (!any) {
            ListMessage("No paired phone was found.");
            return 0;
        }
        Emit(sb.ToString());
        return 0;
    }

    static DeviceWatcher scanWatcher;
    static readonly ManualResetEvent scanStopped = new ManualResetEvent(false);
    static readonly Dictionary<string, DeviceInformation> scanKnown = new Dictionary<string, DeviceInformation>();
    static ulong localAddress;

    static int Scan() {
        try {
            ScanOnce();
        } catch (Exception ex) {
            Error(IsRadioOff(ex) ? "Bluetooth is off." : "Nothing nearby. Put the phone in pairing mode.");
        } finally {
            StopScanWatcher();
        }
        Emit("{\"event\":\"scan-done\"}");
        return 0;
    }

    static void ScanOnce() {
        string off = RadioProblem();
        if (off != null) {
            Error(off);
            return;
        }
        localAddress = 0;
        try {
            BluetoothAdapter adapter = Wait(BluetoothAdapter.GetDefaultAsync(), 8000);
            if (adapter != null) localAddress = adapter.BluetoothAddress;
        } catch (Exception) {
        }
        lock (scanKnown) scanKnown.Clear();
        scanStopped.Reset();
        string selector = "System.Devices.Aep.ProtocolId:=\"{e0cbf06c-cd8b-4647-bb8a-263b43f0f974}\"";
        string[] extra = new string[] {
            "System.Devices.Aep.DeviceAddress",
            "System.Devices.Aep.IsPaired",
            "System.Devices.Aep.IsPresent",
            "System.Devices.FriendlyName",
            "System.ItemNameDisplay",
            "System.Devices.ModelName",
            "System.Software.ProductVersion"
        };
        scanWatcher = DeviceInformation.CreateWatcher(selector, extra, DeviceInformationKind.AssociationEndpoint);
        scanWatcher.Added += OnScanAdded;
        scanWatcher.Updated += OnScanUpdated;
        scanWatcher.Stopped += OnScanStopped;
        scanWatcher.Start();
        scanStopped.WaitOne(20000);
    }

    static void StopScanWatcher() {
        DeviceWatcher watcher = scanWatcher;
        if (watcher == null) return;
        try {
            DeviceWatcherStatus status = watcher.Status;
            if (status == DeviceWatcherStatus.Started || status == DeviceWatcherStatus.EnumerationCompleted || status == DeviceWatcherStatus.Created) {
                watcher.Stop();
                scanStopped.WaitOne(3000);
            }
        } catch (Exception) {
        }
        scanWatcher = null;
    }

    static void OnScanStopped(DeviceWatcher sender, object args) {
        scanStopped.Set();
    }

    static void OnScanAdded(DeviceWatcher sender, DeviceInformation info) {
        if (info == null || string.IsNullOrEmpty(info.Id)) return;
        lock (scanKnown) scanKnown[info.Id] = info;
        EmitDevice(info);
    }

    static void OnScanUpdated(DeviceWatcher sender, DeviceInformationUpdate update) {
        if (update == null || string.IsNullOrEmpty(update.Id)) return;
        DeviceInformation info;
        lock (scanKnown) {
            if (!scanKnown.TryGetValue(update.Id, out info)) return;
        }
        try { info.Update(update); } catch (Exception) { return; }
        EmitDevice(info);
    }

    static void EmitDevice(DeviceInformation info) {
        if (info == null || string.IsNullOrEmpty(info.Id)) return;
        if (!IsPresent(info)) return;
        ulong addr = AddressOf(info);
        if (addr != 0 && addr == localAddress) return;
        string name = info.Name == null ? "" : info.Name.Trim();
        bool paired = false;
        try {
            if (info.Pairing != null) paired = info.Pairing.IsPaired;
        } catch (Exception) {
        }
        object pairedProp = Prop(info, "System.Devices.Aep.IsPaired");
        if (pairedProp is bool && (bool)pairedProp) paired = true;
        string friendly = PropString(info, "System.Devices.FriendlyName");
        if (friendly.Length == 0) friendly = PropString(info, "System.ItemNameDisplay");
        if (string.Equals(friendly, name, StringComparison.OrdinalIgnoreCase)) friendly = "";
        string version = VersionOf(info);
        string address = FormatAddress(addr, Prop(info, "System.Devices.Aep.DeviceAddress"));
        if (name.Length == 0 && friendly.Length == 0 && address.Length == 0) return;
        Emit("{\"event\":\"device\",\"id\":\"" + JsonEscape(info.Id) + "\",\"name\":\"" + JsonEscape(name) + "\",\"friendly\":\"" + JsonEscape(friendly) + "\",\"version\":\"" + JsonEscape(version) + "\",\"address\":\"" + JsonEscape(address) + "\",\"paired\":" + (paired ? "true" : "false") + "}");
    }

    static string PropString(DeviceInformation info, string key) {
        object value = Prop(info, key);
        if (value == null) return "";
        string text = value as string;
        if (text == null) text = Convert.ToString(value);
        if (text == null) return "";
        return text.Trim();
    }

    static string VersionOf(DeviceInformation info) {
        string[] keys = new string[] {
            "System.Software.ProductVersion",
            "System.Devices.Aep.Bluetooth.DeviceVersion"
        };
        foreach (string key in keys) {
            string value = PropString(info, key);
            if (LooksLikeVersion(value)) return value;
        }
        return "";
    }

    static bool LooksLikeVersion(string value) {
        if (string.IsNullOrEmpty(value) || value.Length > 16) return false;
        bool digit = false;
        foreach (char c in value) {
            if (c >= '0' && c <= '9') { digit = true; continue; }
            if (c == '.') continue;
            return false;
        }
        return digit;
    }

    static bool IsPresent(DeviceInformation info) {
        object present = Prop(info, "System.Devices.Aep.IsPresent");
        if (present is bool) return (bool)present;
        if (present is int) return (int)present != 0;
        return true;
    }

    static object Prop(DeviceInformation info, string key) {
        if (info == null) return null;
        try {
            object value;
            if (info.Properties != null && info.Properties.TryGetValue(key, out value)) return value;
        } catch (Exception) {
        }
        try {
            if (info.Properties != null && info.Properties.ContainsKey(key)) return info.Properties[key];
        } catch (Exception) {
        }
        return null;
    }

    static ulong AddressOf(DeviceInformation info) {
        return AddressValue(Prop(info, "System.Devices.Aep.DeviceAddress"));
    }

    static ulong AddressValue(object value) {
        if (value == null) return 0;
        if (value is ulong) return (ulong)value;
        if (value is long) return (ulong)(long)value;
        string text = value as string;
        if (text == null) {
            try { return Convert.ToUInt64(value); } catch (Exception) { return 0; }
        }
        string hex = text.Replace(":", "").Replace("-", "").Trim();
        if (hex.Length == 0) return 0;
        try { return Convert.ToUInt64(hex, 16); } catch (Exception) { return 0; }
    }

    static string FormatAddress(ulong addr, object raw) {
        if (addr == 0) addr = AddressValue(raw);
        if (addr == 0) {
            string text = raw as string;
            return text == null ? "" : text.Trim();
        }
        var sb = new StringBuilder();
        for (int i = 5; i >= 0; i--) {
            if (sb.Length > 0) sb.Append(':');
            sb.Append(((addr >> (8 * i)) & 0xFF).ToString("X2"));
        }
        return sb.ToString();
    }

    static RfcommDeviceService ResolveService(string deviceId) {
        try {
            RfcommDeviceService direct = Wait(RfcommDeviceService.FromIdAsync(deviceId), 8000);
            if (direct != null) return direct;
        } catch (Exception) {
        }
        string[] props = new string[] { "System.Devices.Aep.DeviceAddress", "System.Devices.Aep.IsPaired" };
        DeviceInformation info = null;
        try {
            info = Wait(DeviceInformation.CreateFromIdAsync(deviceId, props, DeviceInformationKind.AssociationEndpoint), 20000);
        } catch (Exception) {
        }
        if (info == null) {
            try { info = Wait(DeviceInformation.CreateFromIdAsync(deviceId), 15000); } catch (Exception) { }
        }
        if (info == null) throw new IOException("missing");
        bool paired = false;
        try {
            if (info.Pairing != null) paired = info.Pairing.IsPaired;
        } catch (Exception) {
        }
        if (!paired) {
            Emit("{\"event\":\"status\",\"message\":\"Accept the pairing prompt on the phone.\"}");
            DevicePairingResult result = Wait(info.Pairing.PairAsync(), 120000);
            if (result == null || (result.Status != DevicePairingResultStatus.Paired && result.Status != DevicePairingResultStatus.AlreadyPaired)) {
                throw new IOException("declined");
            }
        }
        ulong address = AddressOf(info);
        if (address == 0) throw new IOException("missing");
        BluetoothDevice phone = Wait(BluetoothDevice.FromBluetoothAddressAsync(address), 20000);
        if (phone == null) throw new IOException("missing");
        RfcommDeviceServicesResult found = Wait(phone.GetRfcommServicesForIdAsync(RfcommServiceId.ObexObjectPush, BluetoothCacheMode.Uncached), 30000);
        if (found == null || found.Services == null || found.Services.Count < 1) throw new IOException("noopp");
        return found.Services[0];
    }

    static int Send(string[] args) {
        string device = "";
        var files = new System.Collections.Generic.List<string>();
        for (int i = 1; i < args.Length; i++) {
            if (args[i] == "--device" && i + 1 < args.Length) {
                device = args[++i];
                continue;
            }
            if (args[i] == "--file" && i + 1 < args.Length) {
                files.Add(args[++i]);
                continue;
            }
        }
        if (string.IsNullOrWhiteSpace(device)) {
            Error("That phone is not paired.");
            return 1;
        }
        if (files.Count == 0) {
            Error("Select a file");
            return 1;
        }
        string off = RadioProblem();
        if (off != null) {
            Error(off);
            return 1;
        }
        foreach (string path in files) {
            try {
                SendOne(device, path);
            } catch (ObexException) {
                Error("The phone refused the file.");
                return 1;
            } catch (Exception ex) {
                Error(Plain(ex));
                return 1;
            }
        }
        return 0;
    }

    static void SendOne(string device, string path) {
        if (string.IsNullOrWhiteSpace(path)) throw new IOException("notfile");
        var info = new FileInfo(path);
        if (!info.Exists || (info.Attributes & FileAttributes.Directory) != 0) throw new IOException("notfile");
        string name = info.Name;
        if (string.IsNullOrEmpty(name)) name = "file";
        RfcommDeviceService service = ResolveService(device);
        if (service == null) throw new IOException("noopp");
        StreamSocket socket = null;
        try {
            socket = new StreamSocket();
            try {
                Wait(socket.ConnectAsync(
                    service.ConnectionHostName,
                    service.ConnectionServiceName,
                    SocketProtectionLevel.BluetoothEncryptionAllowNullAuthentication), 20000);
            } catch (Exception ex) {
                throw new IOException(Plain(ex));
            }
            var reader = new DataReader(socket.InputStream);
            reader.InputStreamOptions = InputStreamOptions.None;
            var writer = new DataWriter(socket.OutputStream);
            Stream output = new BtWriter(writer);
            int maxPacket;
            byte[] connId;
            ObexConnect(output, delegate { return ReadPacket(reader); }, out maxPacket, out connId);
            using (var fs = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.ReadWrite)) {
                WritePuts(output, fs, fs.Length, name, connId, maxPacket,
                    delegate(int pct) { Progress(name, pct); },
                    delegate(bool last) { return ReadPacket(reader); });
            }
            try {
                output.Write(new byte[] { 0x81, 0x00, 0x03 }, 0, 3);
                output.Flush();
            } catch (Exception) {
            }
        } finally {
            Close(socket);
            Close(service);
        }
        Emit("{\"event\":\"done\",\"name\":\"" + JsonEscape(name) + "\"}");
    }

    static void ObexConnect(Stream output, Func<byte[]> readResponse, out int maxPacket, out byte[] connId) {
        byte[] req = new byte[] { 0x80, 0x00, 0x07, 0x10, 0x00, 0x20, 0x00 };
        output.Write(req, 0, req.Length);
        output.Flush();
        byte[] resp = readResponse();
        if (resp == null || resp.Length < 3 || resp[0] != 0xA0) throw new ObexException(resp != null && resp.Length > 0 ? resp[0] : 0);
        maxPacket = 1024;
        if (resp.Length >= 7) maxPacket = (resp[5] << 8) | resp[6];
        if (maxPacket < 255) maxPacket = 255;
        if (maxPacket > 65535) maxPacket = 65535;
        connId = ParseConnId(resp, 7);
    }

    static void WritePuts(Stream output, Stream file, long size, string fileName, byte[] connId, int maxPacket, Action<int> progress, Func<bool, byte[]> readResponse) {
        if (maxPacket < 255) maxPacket = 255;
        if (maxPacket > 65535) maxPacket = 65535;
        byte[] connHdr = ConnectionHeader(connId);
        byte[] lenHdr = LengthHeader(size);
        int fixedOver = 3 + connHdr.Length + lenHdr.Length + 3;
        int nameBudget = maxPacket - fixedOver;
        if (nameBudget < 8) throw new IOException("packet");
        byte[] nameHdr = NameHeader(FitName(fileName, nameBudget));
        long sent = 0;
        bool first = true;
        while (true) {
            int overhead = 3 + connHdr.Length + 3;
            if (first) overhead += nameHdr.Length + lenHdr.Length;
            int room = maxPacket - overhead;
            if (room < 0) throw new IOException("packet");
            int toRead = (int)Math.Min((long)room, size - sent);
            byte[] chunk = new byte[toRead];
            int got = ReadFull(file, chunk, toRead);
            bool last = sent + got >= size;
            int packetLen = overhead + got;
            byte[] packet = new byte[packetLen];
            int o = 0;
            packet[o++] = last ? (byte)0x82 : (byte)0x02;
            packet[o++] = (byte)(packetLen >> 8);
            packet[o++] = (byte)packetLen;
            if (connHdr.Length > 0) {
                System.Buffer.BlockCopy(connHdr, 0, packet, o, connHdr.Length);
                o += connHdr.Length;
            }
            if (first) {
                System.Buffer.BlockCopy(nameHdr, 0, packet, o, nameHdr.Length);
                o += nameHdr.Length;
                System.Buffer.BlockCopy(lenHdr, 0, packet, o, lenHdr.Length);
                o += lenHdr.Length;
                first = false;
            }
            packet[o++] = last ? (byte)0x49 : (byte)0x48;
            int bodyLen = 3 + got;
            packet[o++] = (byte)(bodyLen >> 8);
            packet[o++] = (byte)bodyLen;
            if (got > 0) System.Buffer.BlockCopy(chunk, 0, packet, o, got);
            output.Write(packet, 0, packet.Length);
            output.Flush();
            if (progress != null) {
                int pct = size == 0 ? 100 : (int)((sent + got) * 100 / size);
                progress(pct);
            }
            byte[] resp = readResponse(last);
            if (resp == null || resp.Length < 1) throw new ObexException(0);
            int code = resp[0] & 0xFF;
            if (last) {
                if (code != 0xA0 && code != 0xA1) throw new ObexException(code);
            } else if (code != 0x90 && code != 0xA0) {
                throw new ObexException(code);
            }
            sent += got;
            if (last) break;
        }
    }

    static int Check() {
        byte[] fileBytes = new byte[300];
        for (int n = 0; n < fileBytes.Length; n++) fileBytes[n] = (byte)(n + 1);
        var output = new MemoryStream();
        var input = new MemoryStream(fileBytes);
        int responses = 0;
        try {
            WritePuts(output, input, fileBytes.Length, "a.bin", new byte[] { 9, 8, 7, 6 }, 255,
                delegate(int pct) { },
                delegate(bool last) {
                    responses++;
                    return new byte[] { last ? (byte)0xA0 : (byte)0x90, 0x00, 0x03 };
                });
        } catch (Exception ex) {
            Emit("{\"ok\":false,\"message\":\"" + JsonEscape(ex.Message) + "\"}");
            return 1;
        }
        byte[] raw = output.ToArray();
        int offset = 0;
        int bodies = 0;
        int packets = 0;
        bool sawName = false;
        bool sawConn = false;
        while (offset < raw.Length) {
            if (offset + 3 > raw.Length) return Fail("short");
            int op = raw[offset];
            int len = (raw[offset + 1] << 8) | raw[offset + 2];
            if (len < 3 || len > 255 || offset + len > raw.Length) return Fail("len");
            int i = offset + 3;
            int end = offset + len;
            while (i < end) {
                int hi = raw[i];
                int kind = hi & 0xC0;
                if (kind == 0xC0) {
                    if (i + 5 > end) return Fail("conn");
                    if (hi == 0xCB) sawConn = true;
                    i += 5;
                } else if (kind == 0x80) {
                    i += 2;
                } else {
                    if (i + 3 > end) return Fail("hdr");
                    int hlen = (raw[i + 1] << 8) | raw[i + 2];
                    if (hlen < 3 || i + hlen > end) return Fail("hlen");
                    if (hi == 0x01) sawName = true;
                    if (hi == 0x48 || hi == 0x49) bodies += hlen - 3;
                    i += hlen;
                }
            }
            bool last = offset + len >= raw.Length;
            if (last && op != 0x82) return Fail("final");
            if (!last && op != 0x02) return Fail("cont");
            offset += len;
            packets++;
        }
        if (!sawName || !sawConn || bodies != fileBytes.Length || packets < 2 || responses != packets) return Fail("framing");
        byte[] name = NameHeader("hi.txt");
        if (name.Length < 5 || name[0] != 0x01 || name[name.Length - 1] != 0 || name[name.Length - 2] != 0) return Fail("name");
        Emit("{\"ok\":true}");
        return 0;
    }

    sealed class BtWriter : Stream {
        readonly DataWriter writer;
        public BtWriter(DataWriter writer) { this.writer = writer; }
        public override bool CanRead { get { return false; } }
        public override bool CanSeek { get { return false; } }
        public override bool CanWrite { get { return true; } }
        public override long Length { get { throw new NotSupportedException(); } }
        public override long Position {
            get { throw new NotSupportedException(); }
            set { throw new NotSupportedException(); }
        }
        public override void Write(byte[] buffer, int offset, int count) {
            if (count <= 0) return;
            byte[] slice = buffer;
            if (offset != 0 || count != buffer.Length) {
                slice = new byte[count];
                System.Buffer.BlockCopy(buffer, offset, slice, 0, count);
            }
            writer.WriteBytes(slice);
        }
        public override void Flush() {
            Wait(writer.StoreAsync(), ReadTimeoutMs);
            Wait(writer.FlushAsync(), ReadTimeoutMs);
        }
        public override int Read(byte[] buffer, int offset, int count) { throw new NotSupportedException(); }
        public override long Seek(long offset, SeekOrigin origin) { throw new NotSupportedException(); }
        public override void SetLength(long value) { throw new NotSupportedException(); }
    }

    static byte[] NameHeader(string name) {
        byte[] chars = Encoding.BigEndianUnicode.GetBytes(name + "\0");
        int len = 3 + chars.Length;
        byte[] h = new byte[len];
        h[0] = 0x01;
        h[1] = (byte)(len >> 8);
        h[2] = (byte)len;
        System.Buffer.BlockCopy(chars, 0, h, 3, chars.Length);
        return h;
    }

    static string FitName(string name, int budget) {
        if (string.IsNullOrEmpty(name)) name = "file";
        int maxChars = (budget - 3) / 2 - 1;
        if (maxChars < 1) maxChars = 1;
        if (name.Length > maxChars) name = name.Substring(0, maxChars);
        return name;
    }

    static byte[] LengthHeader(long size) {
        if (size < 0 || size > uint.MaxValue) return new byte[0];
        uint n = (uint)size;
        return new byte[] { 0xC3, (byte)(n >> 24), (byte)(n >> 16), (byte)(n >> 8), (byte)n };
    }

    static byte[] ConnectionHeader(byte[] connId) {
        if (connId == null || connId.Length != 4) return new byte[0];
        return new byte[] { 0xCB, connId[0], connId[1], connId[2], connId[3] };
    }

    static byte[] ParseConnId(byte[] resp, int i) {
        while (i < resp.Length) {
            int hi = resp[i];
            int kind = hi & 0xC0;
            if (kind == 0xC0) {
                if (i + 5 > resp.Length) return null;
                if (hi == 0xCB) {
                    byte[] id = new byte[4];
                    System.Buffer.BlockCopy(resp, i + 1, id, 0, 4);
                    return id;
                }
                i += 5;
            } else if (kind == 0x80) {
                i += 2;
            } else {
                if (i + 3 > resp.Length) return null;
                int hlen = (resp[i + 1] << 8) | resp[i + 2];
                if (hlen < 3) return null;
                i += hlen;
            }
        }
        return null;
    }

    static byte[] ReadPacket(DataReader reader) {
        byte[] hdr = ReadExact(reader, 3);
        int len = (hdr[1] << 8) | hdr[2];
        if (len < 3 || len > 65535) throw new IOException("closed");
        byte[] pkt = new byte[len];
        System.Buffer.BlockCopy(hdr, 0, pkt, 0, 3);
        if (len > 3) {
            byte[] rest = ReadExact(reader, len - 3);
            System.Buffer.BlockCopy(rest, 0, pkt, 3, rest.Length);
        }
        return pkt;
    }

    static byte[] ReadExact(DataReader reader, int n) {
        uint loaded = Wait(reader.LoadAsync((uint)n), ReadTimeoutMs);
        if (loaded < (uint)n) throw new IOException("closed");
        byte[] buf = new byte[n];
        reader.ReadBytes(buf);
        return buf;
    }

    static int ReadFull(Stream file, byte[] chunk, int toRead) {
        int got = 0;
        while (got < toRead) {
            int r = file.Read(chunk, got, toRead - got);
            if (r <= 0) break;
            got += r;
        }
        return got;
    }

    static bool IsPaired(DeviceInformation info) {
        try {
            if (info.Pairing == null) return true;
            return info.Pairing.IsPaired;
        } catch (Exception) {
            return true;
        }
    }

    static string RadioProblem() {
        try {
            var radios = Wait(Radio.GetRadiosAsync(), 15000);
            bool any = false;
            bool on = false;
            if (radios != null) {
                int count = radios.Count;
                for (int i = 0; i < count; i++) {
                    Radio radio = radios[i];
                    if (radio == null || radio.Kind != RadioKind.Bluetooth) continue;
                    any = true;
                    if (radio.State == RadioState.On) on = true;
                }
            }
            if (any && !on) return "Bluetooth is off.";
            if (any && on) return null;
        } catch (Exception) {
        }
        try {
            BluetoothAdapter adapter = Wait(BluetoothAdapter.GetDefaultAsync(), 15000);
            if (adapter == null) return "Bluetooth is off.";
        } catch (Exception ex) {
            if (IsRadioOff(ex)) return "Bluetooth is off.";
        }
        return null;
    }

    static bool IsRadioOff(Exception ex) {
        string text = ex == null ? "" : ex.ToString();
        return text.IndexOf("turned off", StringComparison.OrdinalIgnoreCase) >= 0
            || text.IndexOf("radio is off", StringComparison.OrdinalIgnoreCase) >= 0
            || text.IndexOf("Bluetooth is off", StringComparison.OrdinalIgnoreCase) >= 0;
    }

    static string Plain(Exception ex) {
        if (ex is ObexException) return "The phone refused the file.";
        string text = ex == null ? "" : (ex.Message ?? "");
        if (text.IndexOf("declined", StringComparison.OrdinalIgnoreCase) >= 0
            || text.IndexOf("cancel", StringComparison.OrdinalIgnoreCase) >= 0
            || text.IndexOf("rejected", StringComparison.OrdinalIgnoreCase) >= 0) return "Pairing was declined.";
        if (text.IndexOf("noopp", StringComparison.OrdinalIgnoreCase) >= 0) return "This phone does not accept Bluetooth files.";
        if (text.IndexOf("missing", StringComparison.OrdinalIgnoreCase) >= 0) return "That phone is not nearby.";
        if (text.IndexOf("timeout", StringComparison.OrdinalIgnoreCase) >= 0) return "The phone did not answer.";
        if (IsRadioOff(ex)) return "Bluetooth is off.";
        if (text.IndexOf("notfile", StringComparison.OrdinalIgnoreCase) >= 0) return "That file could not be sent.";
        return "The phone refused the file.";
    }

    static void ListMessage(string msg) {
        Emit("{\"ok\":false,\"message\":\"" + JsonEscape(msg) + "\",\"devices\":[]}");
    }

    static void Progress(string name, int pct) {
        if (pct < 0) pct = 0;
        if (pct > 100) pct = 100;
        Emit("{\"event\":\"progress\",\"name\":\"" + JsonEscape(name) + "\",\"percent\":" + pct.ToString() + "}");
    }

    static void Error(string msg) {
        Emit("{\"event\":\"error\",\"message\":\"" + JsonEscape(msg) + "\"}");
    }

    static int Fail(string msg) {
        Emit("{\"ok\":false,\"message\":\"" + JsonEscape(msg) + "\"}");
        return 1;
    }

    static readonly object emitLock = new object();

    static void Emit(string line) {
        lock (emitLock) {
            Console.Out.WriteLine(line);
            Console.Out.Flush();
        }
    }

    static string JsonEscape(string s) {
        if (string.IsNullOrEmpty(s)) return "";
        var sb = new StringBuilder();
        foreach (char c in s) {
            switch (c) {
                case '\\': sb.Append("\\\\"); break;
                case '"': sb.Append("\\\""); break;
                case '\n': sb.Append("\\n"); break;
                case '\r': break;
                case '\t': sb.Append("\\t"); break;
                default:
                    if (c < 32) sb.Append("\\u").Append(((int)c).ToString("x4"));
                    else sb.Append(c);
                    break;
            }
        }
        return sb.ToString();
    }

    static void Close(object obj) {
        if (obj == null) return;
        var flags = System.Reflection.BindingFlags.Instance | System.Reflection.BindingFlags.Public | System.Reflection.BindingFlags.NonPublic;
        var method = obj.GetType().GetMethod("Close", flags, null, Type.EmptyTypes, null);
        if (method == null) method = obj.GetType().GetMethod("Dispose", flags, null, Type.EmptyTypes, null);
        if (method == null) return;
        try { method.Invoke(obj, null); } catch (Exception) { }
    }

    static T Wait<T>(IAsyncOperation<T> op, int timeoutMs) {
        if (op == null) throw new IOException("closed");
        Exception error = null;
        var done = new ManualResetEvent(false);
        op.Completed = delegate(IAsyncOperation<T> info, AsyncStatus status) {
            try {
                if (status == AsyncStatus.Error) error = info.ErrorCode;
                else if (status == AsyncStatus.Canceled) error = new IOException("closed");
            } catch (Exception ex) {
                error = ex;
            }
            done.Set();
        };
        if (!done.WaitOne(timeoutMs)) throw new IOException("timeout");
        if (error != null) throw error;
        return op.GetResults();
    }

    static void Wait(IAsyncAction op, int timeoutMs) {
        if (op == null) throw new IOException("closed");
        Exception error = null;
        var done = new ManualResetEvent(false);
        op.Completed = delegate(IAsyncAction info, AsyncStatus status) {
            try {
                if (status == AsyncStatus.Error) error = info.ErrorCode;
                else if (status == AsyncStatus.Canceled) error = new IOException("closed");
            } catch (Exception ex) {
                error = ex;
            }
            done.Set();
        };
        if (!done.WaitOne(timeoutMs)) throw new IOException("timeout");
        if (error != null) throw error;
    }
}

class ObexException : Exception {
    public ObexException(int code) : base("obex") { }
}
