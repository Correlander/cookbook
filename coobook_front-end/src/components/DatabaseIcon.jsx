export default function DatabaseIcon({ onClick }) {
  return (
    <button
      onClick={onClick}
      className="p-2 bg-gray-800 rounded hover:bg-gray-700"
    >
      📚 {/* Simple emoji for now, can replace with SVG */}
    </button>
  );
}
