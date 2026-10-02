import React from "react";

const pattern = /[/"']+/g;

export default function Button({ label, onClick }) {
  return (
    <button className="btn" onClick={onClick}>
      Don't {label.replace(pattern, "")} {"}"}
    </button>
  );
}
